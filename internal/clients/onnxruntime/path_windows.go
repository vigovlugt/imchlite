//go:build windows

package onnxruntime

import (
	"syscall"
	"unsafe"
)

// cPath converts path to the NUL-terminated ORTCHAR_T string the C API takes
// for paths, which is UTF-16 on Windows. The string holds no Go pointers, so
// it may be passed to C directly; the release func does nothing.
func cPath(path string) (unsafe.Pointer, func(), error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, func() {}, err
	}
	return unsafe.Pointer(p), func() {}, nil
}
