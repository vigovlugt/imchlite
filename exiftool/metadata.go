package exiftool

import (
	"fmt"
	"log"
	"mime"
	"os"
	"path"
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

// MimeTypeFromPath guesses a file's mime type from its extension; it is
// used as a fallback when exiftool reports none.
func MimeTypeFromPath(p string) string {
	ext, ok := lowerExtension(p)
	if !ok {
		return ""
	}
	return mime.TypeByExtension(ext)
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