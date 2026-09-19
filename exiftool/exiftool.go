package exiftool

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vigovlugt/imchlite/cachedir"
)

type Exiftool struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Scanner
	counter int
	mu      sync.Mutex
}

func Setup() (string, error) {
	if len(filesRoot) == 0 {
		return "", fmt.Errorf("no embedded exiftool distribution for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	return cachedir.Ensure("exiftool", func(dir string) error {
		return writeTree(files, filesRoot, dir)
	})
}

// New starts an exiftool process using the distribution directory created
// by Setup. Each instance is single-threaded; use one instance per goroutine.
func New(dir string) (*Exiftool, error) {
	cmd := exec.Command(filepath.Join(dir, extractedFilename), "-stay_open", "True", "-@", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open exiftool stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open exiftool stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open exiftool stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start exiftool: %w", err)
	}

	e := &Exiftool{}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("exiftool: %s", scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			log.Printf("exiftool stderr: %v", err)
		}
	}()

	e.cmd = cmd
	e.stdin = stdin
	e.stdout = bufio.NewScanner(stdout)

	return e, nil
}

func (e *Exiftool) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cmd != nil && e.cmd.Process != nil {
		fmt.Fprintln(e.stdin, "-stay_open")
		fmt.Fprintln(e.stdin, "False")

		done := make(chan struct{})
		go func() {
			e.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			e.cmd.Process.Kill()
			<-done
		}
	}
	return nil
}

func (e *Exiftool) run(args ...string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.counter++
	ready := "{ready" + strconv.Itoa(e.counter) + "}"

	for _, arg := range append(args, "-execute"+strconv.Itoa(e.counter)) {
		if _, err := fmt.Fprintln(e.stdin, arg); err != nil {
			return nil, fmt.Errorf("talk to exiftool: %w", err)
		}
	}

	var response []byte
	for e.stdout.Scan() {
		if e.stdout.Text() == ready {
			return response, nil
		}
		response = append(response, e.stdout.Bytes()...)
		response = append(response, '\n')
	}
	if err := e.stdout.Err(); err != nil {
		return nil, fmt.Errorf("read exiftool: %w", err)
	}
	return nil, fmt.Errorf("exiftool exited before responding")
}

type MediaMetadata struct {
	Width        int64
	Height       int64
	DurationMs   int64
	LocalTakenAt int64
	TakenAtUTC   int64
	Orientation  int64
	MimeType     string

	TimeZone  string
	Latitude  float64
	Longitude float64
	City      string
	Country   string
}

func (e *Exiftool) ProbeMetadata(path string) (MediaMetadata, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return MediaMetadata{}, fmt.Errorf("exiftool %s: %w", path, err)
		}
		return MediaMetadata{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return MediaMetadata{}, fmt.Errorf("exiftool %s: not a regular file", path)
	}

	response, err := e.run(
		"-api", "Geolocation",
		"-api", "GeolocFeature=-PPLX,-PPLH",
		"-n", "-j", path,
	)
	if err != nil {
		return MediaMetadata{}, fmt.Errorf("exiftool %s: %w", path, err)
	}

	var results []map[string]any
	if err := json.Unmarshal(response, &results); err != nil {
		return MediaMetadata{}, fmt.Errorf("parse exiftool output for %s: %w", path, err)
	}
	if len(results) == 0 {
		return MediaMetadata{}, fmt.Errorf("exiftool returned no metadata for %s", path)
	}

	fields := results[0]

	var m MediaMetadata
	m.Width = getIntField(fields, "ImageWidth", "ExifImageWidth")
	m.Height = getIntField(fields, "ImageHeight", "ExifImageHeight")

	if seconds, err := fieldFloat(fields, "Duration"); err == nil {
		m.DurationMs = int64(seconds * 1000)
	}

	m.Orientation, _ = fieldInt(fields, "Orientation")
	m.MimeType, _ = fieldString(fields, "MIMEType")

	m.Latitude, _ = fieldFloat(fields, "GPSLatitude")
	m.Longitude, _ = fieldFloat(fields, "GPSLongitude")

	m.TimeZone, _ = fieldString(fields, "TimeZone")
	m.City, _ = fieldString(fields, "City")
	m.Country, _ = fieldString(fields, "Country")

	if m.City == "" || m.Country == "" {
		if m.Latitude != 0 || m.Longitude != 0 {
			if m.City == "" {
				m.City, _ = fieldString(fields, "GeolocationCity")
			}
			if m.Country == "" {
				m.Country, _ = fieldString(fields, "GeolocationCountry")
			}
		}
	}

	// Capture-time tag priority mirrors the Immich server's firstDateTime
	// list. EXIF date tags hold a wall clock without zone info; QuickTime
	// tags hold a UTC instant. QuickTime CreationDate is the exception:
	// Apple writes it with an explicit UTC offset.
	isVideo := false
	if _, err := fieldFloat(fields, "Duration"); err == nil {
		isVideo = true
	}
	if _, ok := fields["TrackCreateDate"]; ok {
		isVideo = true
	}

	// Wall-clock candidates for both kinds. CreateDate and its composites
	// are only wall clocks for still images: for videos CreateDate is
	// QuickTime and means a UTC instant.
	var wallTime time.Time
	wallKeys := []string{"SubSecDateTimeOriginal", "DateTimeOriginal"}
	if !isVideo {
		wallKeys = append(wallKeys, "SubSecCreateDate", "CreateDate", "MediaCreateDate", "DateTimeCreated", "DateCreated")
	}
	for _, key := range wallKeys {
		if value, ok := fieldString(fields, key); ok {
			if taken, ok := parseExifDate(value); ok {
				wallTime = taken
				break
			}
		}
	}

	// Apple's QuickTime CreationDate carries the wall clock together with
	// its UTC offset, so it fills both columns and pins the zone.
	creationZoned := false
	if wallTime.IsZero() {
		if value, ok := fieldString(fields, "CreationDate"); ok {
			if taken, zonedOffset, ok := parseZonedDate(value); ok {
				m.LocalTakenAt = taken.Unix()
				m.TakenAtUTC = taken.Add(-zonedOffset).Unix()
				if m.TimeZone == "" {
					m.TimeZone = formatZoneOffset(zonedOffset)
				}
				creationZoned = true
			}
		}
	}

	// Camera-assigned UTC offset (photos with GPS-capable clocks).
	var offset time.Duration
	hasOffset := false
	for _, key := range []string{"OffsetTimeOriginal", "OffsetTimeDigitized", "OffsetTime"} {
		if value, ok := fieldString(fields, key); ok {
			if d, err := parseZoneOffset(value); err == nil {
				offset = d
				hasOffset = true
				break
			}
		}
	}
	if hasOffset {
		m.TimeZone = formatZoneOffset(offset)
	}

	switch {
	case !wallTime.IsZero():
		// The wall clock is known; the offset yields the true instant.
		m.LocalTakenAt = wallTime.Unix()
		if hasOffset {
			m.TakenAtUTC = wallTime.Add(-offset).Unix()
		}
	case creationZoned:
		// Both columns were set from CreationDate's offset.
	case isVideo:
		// QuickTime CreateDate and its composites hold a UTC instant
		// (exiftool already decoded them above). Some older devices
		// write local time in violation of the spec.
		for _, key := range []string{"SubSecCreateDate", "CreateDate", "MediaCreateDate", "DateTimeUTC", "SonyDateTime2"} {
			if value, ok := fieldString(fields, key); ok {
				if taken, ok := parseExifDate(value); ok {
					m.TakenAtUTC = taken.Unix()
					break
				}
			}
		}
	}

	// GPS timestamps are always UTC, but only trusted when the camera's
	// own clock data is missing: re-saved files can carry a GPS track
	// stamped with the re-save date. DateTimeUTC and SonyDateTime2 are
	// also plain UTC instants, kept after GPSDateTime to match the Immich
	// server's priority.
	if m.TakenAtUTC == 0 && m.LocalTakenAt == 0 {
		for _, key := range []string{"GPSDateTime", "DateTimeUTC", "SonyDateTime2"} {
			if value, ok := fieldString(fields, key); ok {
				if taken, ok := parseGPSDate(value); ok {
					m.TakenAtUTC = taken.Unix()
					break
				}
			}
		}
	}

	return m, nil
}

func (e *Exiftool) ReverseGeocode(latitude, longitude float64) (string, string, error) {
	pos := strconv.FormatFloat(latitude, 'f', -1, 64) + "," + strconv.FormatFloat(longitude, 'f', -1, 64)

	response, err := e.run(
		"-api", "GeolocFeature=-PPLX,-PPLH",
		"-api", "Geolocation="+pos,
		"-GeolocationCity", "-GeolocationCountry", "-j",
	)
	if err != nil {
		return "", "", fmt.Errorf("exiftool reverse geocode: %w", err)
	}

	var results []struct {
		GeolocationCity    string `json:"GeolocationCity"`
		GeolocationCountry string `json:"GeolocationCountry"`
	}
	if err := json.Unmarshal(response, &results); err != nil {
		return "", "", fmt.Errorf("parse reverse geocode output: %w", err)
	}
	if len(results) == 0 {
		return "", "", nil
	}
	return results[0].GeolocationCity, results[0].GeolocationCountry, nil
}

func MimeTypeFromPath(p string) string {
	ext, ok := lowerExtension(p)
	if !ok {
		return ""
	}
	return mime.TypeByExtension(ext)
}

func writeTree(files embed.FS, root, dest string) error {
	return fs.WalkDir(files, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk embedded exiftool tree: %w", err)
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := files.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		if err := os.WriteFile(target, data, 0o755); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}

func lowerExtension(p string) (string, bool) {
	ext := path.Ext(p)
	if ext == "" {
		return "", false
	}
	return strings.ToLower(ext), true
}

func fieldString(fields map[string]any, key string) (string, bool) {
	switch v := fields[key].(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

func fieldFloat(fields map[string]any, key string) (float64, error) {
	switch v := fields[key].(type) {
	case nil:
		return 0, fmt.Errorf("field %s not found", key)
	case float64:
		return v, nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("parse float %s=%q: %w", key, v, err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("field %s is not numeric", key)
	}
}

func fieldInt(fields map[string]any, key string) (int64, error) {
	f, err := fieldFloat(fields, key)
	if err != nil {
		return 0, err
	}
	return int64(f), nil
}

func getIntField(fields map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, err := fieldInt(fields, key); err == nil {
			return value
		}
	}
	return 0
}

func parseExifDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	// SubSec composites append fractional seconds; only whole seconds
	// are kept.
	if len(value) > 19 && (value[19] == '.' || value[19] == ',') {
		value = value[:19]
	}
	t, err := time.ParseInLocation("2006:01:02 15:04:05", value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parseZonedDate parses a date/time with an explicit UTC offset, as written
// by Apple's QuickTime CreationDate, e.g. "2023-10-06T08:39:09+0200" or
// "2023:10:06 08:39:09+02:00".
func parseZonedDate(value string) (time.Time, time.Duration, bool) {
	value = strings.TrimSpace(value)
	layouts := []string{
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999-0700",
		"2006:01:02 15:04:05Z07:00",
		"2006:01:02 15:04:05-07:00",
		"2006:01:02 15:04:05-0700",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			_, offset := t.Zone()
			return t, time.Duration(offset) * time.Second, true
		}
	}
	return time.Time{}, 0, false
}

// parseZoneOffset parses an EXIF offset tag such as "+02:00".
func parseZoneOffset(value string) (time.Duration, error) {
	s := strings.TrimSpace(value)
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return 0, fmt.Errorf("invalid offset %q", value)
	}
	hours, err := strconv.Atoi(s[1:3])
	if err != nil {
		return 0, fmt.Errorf("invalid offset %q: %w", value, err)
	}
	minutes, err := strconv.Atoi(s[4:6])
	if err != nil {
		return 0, fmt.Errorf("invalid offset %q: %w", value, err)
	}
	d := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	if s[0] == '-' {
		d = -d
	}
	return d, nil
}

// formatZoneOffset renders a duration as an EXIF-style offset such as "+02:00".
func formatZoneOffset(d time.Duration) string {
	sign := "+"
	if d < 0 {
		sign = "-"
		d = -d
	}
	return fmt.Sprintf("%s%02d:%02d", sign, int(d.Hours()), int(d.Minutes())%60)
}

// parseGPSDate parses a GPS date/time value such as "2022:11:11 19:23:54Z".
func parseGPSDate(value string) (time.Time, bool) {
	value = strings.TrimSuffix(strings.TrimSpace(value), "Z")
	t, err := time.ParseInLocation("2006:01:02 15:04:05", value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
