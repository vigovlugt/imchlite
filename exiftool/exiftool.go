// Package exiftool embeds the exiftool distribution and extracts it to a
// temporary directory at runtime, so go-exiftool can drive the binary
// without exiftool being installed on the host.
package exiftool

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	goexiftool "github.com/barasher/go-exiftool"
)

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
