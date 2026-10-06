// Package onnxruntime manages the embedded ONNX Runtime shared library. Like
// the ffmpeg package, the library is embedded in the binary and extracted to
// the user cache directory on first use, where it persists between runs.
//
// Next to the CPU build of the runtime, the WebGPU plugin execution provider
// is embedded. Sessions run on the GPU through it (Vulkan on Linux, Direct3D
// 12 on Windows) when a GPU adapter is available, and on the CPU otherwise.
package onnxruntime

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
	"github.com/vigovlugt/imchlite/internal/cachedir"
)

// cacheName is the cache directory the libraries are extracted to. It names
// the build so a cache populated by an earlier build is not reused.
const cacheName = "onnxruntime-1.30.0-webgpu-0.4.0"

const webGPUEpName = "WebGpuExecutionProvider"

// webGPUDevice is the device sessions run on through the WebGPU execution
// provider, or nil if there is none. It is set by Setup, once.
var (
	webGPUDevice *gpuDevice
	webGPUOnce   sync.Once
)

// Setup initializes the ONNX Runtime, extracting the embedded shared libraries
// to the user cache directory on first use and loading them from there. The
// directory persists between runs.
func Setup() error {
	entries, _ := binFS.ReadDir(binDir)
	if len(entries) == 0 {
		return fmt.Errorf("no embedded onnxruntime library for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	dir, err := cachedir.Ensure(cacheName, func(dir string) error {
		for _, entry := range entries {
			data, err := fs.ReadFile(binFS, path.Join(binDir, entry.Name()))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, entry.Name()), data, 0o755); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	ort.SetSharedLibraryPath(filepath.Join(dir, libraryFilename))
	if err := ort.Init(); err != nil {
		return fmt.Errorf("init onnxruntime: %w", err)
	}

	// A plugin library can only be registered once per process, while Setup
	// may run several times.
	webGPUOnce.Do(func() {
		webGPUDevice, err = registerWebGPU(filepath.Join(dir, libraryFilename), filepath.Join(dir, webGPUFilename))
		if err != nil {
			log.Printf("info: webgpu unavailable, onnxruntime runs on cpu: %v", err)
		} else {
			log.Printf("info: found webgpu gpu (vendor %#04x, device %#04x)", webGPUDevice.vendorID, webGPUDevice.deviceID)
		}
	})

	return nil
}

// mu serializes all session use. WebGPU sessions share one device context,
// which crashes when two sessions use it at the same time, whether creating,
// running or closing them. CPU sessions take it too, for simplicity.
//
// TODO: microsoft/onnxruntime#32587 makes separate WebGPU sessions safe to
// use concurrently. It was merged after WebGPU plugin 0.4.0 and needs ONNX
// Runtime >= 1.30.1. Once a plugin release includes it, bump the embedded
// runtime and plugin (and cacheName), then replace this global lock with a
// per-session one: the PR does not cover parallel Run calls on a single
// session, and every clip worker shares one. Test the change carefully:
// concurrent use without the fix faulted the GPU (Xid 31/32/79) and once
// needed a reboot.
var mu sync.Mutex

// Session is an inference session created by NewSession. It only exposes
// methods that hold mu.
type Session struct {
	session *ort.Session
}

// NewSession creates an inference session for the model at path on the GPU
// through WebGPU, falling back to the CPU if there is no GPU or the session
// cannot be created on it, for example because no Vulkan driver is installed.
// Setup must have succeeded first.
func NewSession(path string) (*Session, error) {
	mu.Lock()
	defer mu.Unlock()

	session, err := newWebGPUSession(path)
	if err != nil {
		log.Printf("warn: create webgpu session for %s, falling back to cpu: %v", filepath.Base(path), err)
	}
	if session == nil {
		if session, err = ort.NewSession(path, nil); err != nil {
			return nil, err
		}
	}
	return &Session{session: session}, nil
}

// newWebGPUSession creates a session on the WebGPU device. It returns a nil
// session and no error if there is no WebGPU device.
func newWebGPUSession(path string) (*ort.Session, error) {
	if webGPUDevice == nil {
		return nil, nil
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer func() { _ = opts.Close() }()
	if err := appendDevice(opts, webGPUDevice); err != nil {
		return nil, err
	}
	return ort.NewSession(path, opts)
}

// Run runs the session, as ort.Session.Run does, and also returns how long
// it waited for other sessions' use to finish first. The outputs are in CPU
// memory, so reading and closing them needs no lock.
func (s *Session) Run(ctx context.Context, inputs map[string]*ort.Tensor, outputNames []string) (map[string]*ort.Tensor, time.Duration, error) {
	start := time.Now()
	mu.Lock()
	defer mu.Unlock()
	wait := time.Since(start)
	outputs, err := s.session.Run(ctx, inputs, outputNames)
	return outputs, wait, err
}

// Close releases the session.
func (s *Session) Close() error {
	mu.Lock()
	defer mu.Unlock()
	return s.session.Close()
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

// Shutdown releases the ONNX Runtime environment. Every session must be
// closed first.
func Shutdown() error {
	if err := ort.Shutdown(); err != nil {
		return err
	}
	if webGPUDevice != nil {
		webGPUDevice = nil
		return unregisterWebGPU()
	}
	return nil
}
