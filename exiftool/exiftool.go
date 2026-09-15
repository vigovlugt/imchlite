// Package exiftool embeds the exiftool distribution and extracts it to a
// temporary directory at runtime, so go-exiftool can drive the binary
// without exiftool being installed on the host.
package exiftool

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
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
	"time"

	goexiftool "github.com/barasher/go-exiftool"
)

// Exiftool wraps the extracted exiftool binary and the go-exiftool driver
// talking to it.
type Exiftool struct {
	dir string
	et  *goexiftool.Exiftool
}

// Extract writes the embedded exiftool distribution to a temporary directory,
// starts the go-exiftool driver against it and returns the ready-to-use
// exiftool instance.
func Extract() (*Exiftool, error) {
	if len(filesRoot) == 0 {
		return nil, fmt.Errorf("no embedded exiftool distribution for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	dir, err := os.MkdirTemp("", "imchlite-exiftool-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	e := &Exiftool{dir: dir}
	if err := writeTree(files, filesRoot, dir); err != nil {
		e.Close()
		return nil, err
	}

	et, err := goexiftool.NewExiftool(
		goexiftool.SetExiftoolBinaryPath(filepath.Join(dir, extractedFilename)),
		goexiftool.NoPrintConversion(),
		// Activates exiftool's reverse geocoder, which fills the
		// Geolocation* tags from the embedded database. Neighborhood
		// (PPLX) and historical (PPLH) place entries are excluded so
		// cities resolve to proper populated places such as NYC.
		goexiftool.Api("Geolocation"),
		goexiftool.Api("GeolocFeature=-PPLX,-PPLH"),
	)
	if err != nil {
		e.Close()
		return nil, fmt.Errorf("init exiftool: %w", err)
	}
	e.et = et

	return e, nil
}

// Close shuts down the exiftool process and removes the extracted files.
func (e *Exiftool) Close() error {
	err := e.et.Close()
	if rerr := os.RemoveAll(e.dir); err == nil {
		err = rerr
	}
	return err
}

// MediaMetadata holds the fields exiftool could extract from a file; every
// field is zero when unknown.
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

// ProbeMetadata extracts dimensions, duration, capture time, orientation and
// mime type via exiftool. Failures are non-fatal: the asset is still stored
// with whatever was found.
func (e *Exiftool) ProbeMetadata(path string) (MediaMetadata, error) {
	metas := e.et.ExtractMetadata(path)
	if len(metas) == 0 {
		log.Printf("exiftool returned no metadata for %s", path)
		return MediaMetadata{}, fmt.Errorf("exiftool returned no metadata for %s", path)
	}

	meta := metas[0]
	if meta.Err != nil {
		log.Printf("exiftool %s: %v", path, meta.Err)
		return MediaMetadata{}, fmt.Errorf("exiftool %s: %v", path, meta.Err)
	}

	var m MediaMetadata
	m.Width = getIntField(meta, "ImageWidth", "ExifImageWidth")
	m.Height = getIntField(meta, "ImageHeight", "ExifImageHeight")

	if seconds, err := meta.GetFloat("Duration"); err == nil {
		m.DurationMs = int64(seconds * 1000)
	}

	m.Orientation, _ = meta.GetInt("Orientation")
	m.MimeType, _ = meta.GetString("MIMEType")

	// GPSLatitude/GPSLongitude are signed numbers under -n (no print
	// conversion), so hemisphere signs come for free.
	m.Latitude, _ = meta.GetFloat("GPSLatitude")
	m.Longitude, _ = meta.GetFloat("GPSLongitude")

	m.TimeZone, _ = meta.GetString("TimeZone")
	m.City, _ = meta.GetString("City")
	m.Country, _ = meta.GetString("Country")

	// Fall back to exiftool's reverse geocoder when the file carries GPS
	// coordinates but no city/country tags of its own.
	if m.City == "" || m.Country == "" {
		if m.Latitude != 0 || m.Longitude != 0 {
			if m.City == "" {
				m.City, _ = meta.GetString("GeolocationCity")
			}
			if m.Country == "" {
				m.Country, _ = meta.GetString("GeolocationCountry")
			}
		}
	}

	for _, key := range []string{"DateTimeOriginal", "CreateDate"} {
		if value, err := meta.GetString(key); err == nil {
			if taken, ok := parseExifDate(value); ok {
				// Capture times carry no reliable zone; store the wall
				// clock pinned to UTC so it survives any server TZ.
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

// ReverseGeocode resolves the city and country nearest to the given
// coordinates with exiftool's embedded geolocation database. No media input
// is involved: the coordinates are passed through the API Geolocation
// option's default value and exiftool runs without any input file.
func (e *Exiftool) ReverseGeocode(latitude, longitude float64) (string, string, error) {
	pos := strconv.FormatFloat(latitude, 'f', -1, 64) + "," + strconv.FormatFloat(longitude, 'f', -1, 64)

	cmd := exec.Command(
		filepath.Join(e.dir, extractedFilename),
		"-api", "Geolocation="+pos,
		// Same place filtering as the Extract() driver setup:
		// neighborhood (PPLX) and historical (PPLH) entries excluded
		// so cities resolve to proper populated places.
		"-api", "GeolocFeature=-PPLX,-PPLH",
		"-GeolocationCity", "-GeolocationCountry", "-j",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("exiftool reverse geocode: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	var results []struct {
		GeolocationCity    string `json:"GeolocationCity"`
		GeolocationCountry string `json:"GeolocationCountry"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		return "", "", fmt.Errorf("parse reverse geocode output: %w", err)
	}
	if len(results) == 0 {
		return "", "", nil
	}
	return results[0].GeolocationCity, results[0].GeolocationCountry, nil
}

// MimeTypeFromPath guesses a file's mime type from its extension; it is
// used as a fallback when exiftool reports none.
func MimeTypeFromPath(p string) string {
	ext, ok := lowerExtension(p)
	if !ok {
		return ""
	}
	return mime.TypeByExtension(ext)
}

// writeTree materializes the embedded tree rooted at root into dest,
// preserving relative paths and marking every file executable since embed.FS
// does not carry permission bits.
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

// lowerExtension returns the lowercased extension of p, false when it has
// none.
func lowerExtension(p string) (string, bool) {
	ext := path.Ext(p)
	if ext == "" {
		return "", false
	}
	return strings.ToLower(ext), true
}

// getIntField returns the first present integer field among the keys.
func getIntField(meta goexiftool.FileMetadata, keys ...string) int64 {
	for _, key := range keys {
		if value, err := meta.GetInt(key); err == nil {
			return value
		}
	}
	return 0
}

// parseExifDate parses exiftool's date strings such as
// "2026:07:09 12:34:56". The location is arbitrary: only the wall-clock
// fields are ever used, since EXIF carries no timezone.
func parseExifDate(value string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006:01:02 15:04:05", value, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
