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
)

type Exiftool struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Scanner
	counter int
	mu      sync.Mutex
}

// Setup extracts the embedded exiftool distribution into a temporary
// directory and returns its path. The returned dir must be cleaned up with
// Teardown.
func Setup() (string, error) {
	if len(filesRoot) == 0 {
		return "", fmt.Errorf("no embedded exiftool distribution for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	dir, err := os.MkdirTemp("", "imchlite-exiftool-")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}

	if err := writeTree(files, filesRoot, dir); err != nil {
		Teardown(dir)
		return "", err
	}

	return dir, nil
}

// Teardown removes the exiftool distribution directory created by Setup.
func Teardown(dir string) error {
	return os.RemoveAll(dir)
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
	if e.cmd != nil && e.cmd.Process != nil {
		fmt.Fprintln(e.stdin, "-stay_open")
		fmt.Fprintln(e.stdin, "False")

		done := make(chan struct{})
	e := &Exiftool{}

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

	for _, key := range []string{"DateTimeOriginal", "CreateDate"} {
		if value, ok := fieldString(fields, key); ok {
			if taken, ok := parseExifDate(value); ok {
				m.LocalTakenAt = time.Date(
					taken.Year(), taken.Month(), taken.Day(),
					taken.Hour(), taken.Minute(), taken.Second(), 0, time.UTC,
				).Unix()
				break
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
	t, err := time.ParseInLocation("2006:01:02 15:04:05", value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
