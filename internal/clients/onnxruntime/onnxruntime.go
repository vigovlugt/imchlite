// Package onnxruntime manages the embedded ONNX Runtime shared library. Like
// the ffmpeg package, the library is embedded in the binary and extracted to
// the user cache directory on first use, where it persists between runs.
package onnxruntime

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

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

// Runtime is an ONNX Runtime initialized in the background by Load.
type Runtime struct {
	// ready is closed once initialization finished; err is set before.
	ready chan struct{}
	err   error
}

// Load starts initializing the ONNX Runtime in the background, as Setup
// does. Loading the shared library takes tens of milliseconds, so callers
// that create sessions should Wait for it first instead of blocking startup.
func Load() *Runtime {
	r := &Runtime{ready: make(chan struct{})}
	go func() {
		defer close(r.ready)

		start := time.Now()
		r.err = Setup()
		if r.err == nil {
			log.Printf("debug: onnxruntime initialized in %s", time.Since(start))
		}
	}()
	return r
}

// Wait blocks until the background initialization finished and returns its
// error, if any.
func (r *Runtime) Wait() error {
	<-r.ready
	return r.err
}

// Shutdown releases the ONNX Runtime environment.
func Shutdown() error {
	return ort.Shutdown()
}
