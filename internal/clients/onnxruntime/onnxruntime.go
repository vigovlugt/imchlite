// Package onnxruntime manages the embedded ONNX Runtime shared library. Like
// the ffmpeg package, the library is embedded in the binary and extracted to
// the user cache directory on first use, where it persists between runs.
package onnxruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
	"github.com/vigovlugt/imchlite/internal/cachedir"
)

// Setup initializes the ONNX Runtime, extracting the embedded shared library to
// the user cache directory on first use and loading it from there. The
// directory persists between runs.
func Setup() error {
	if len(libraryBytes) == 0 {
		return fmt.Errorf("no embedded onnxruntime library for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	dir, err := cachedir.Ensure("onnxruntime", func(dir string) error {
		return os.WriteFile(filepath.Join(dir, libraryFilename), libraryBytes, 0o755)
	})
	if err != nil {
		return err
	}

	ort.SetSharedLibraryPath(filepath.Join(dir, libraryFilename))
	if err := ort.Init(); err != nil {
		return fmt.Errorf("init onnxruntime: %w", err)
	}

	return nil
}

// Shutdown releases the ONNX Runtime environment.
func Shutdown() error {
	return ort.Shutdown()
}
