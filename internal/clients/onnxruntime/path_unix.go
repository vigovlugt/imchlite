//go:build !windows

package onnxruntime

// #include <stdlib.h>
import "C"

import "unsafe"

// cPath converts path to the NUL-terminated ORTCHAR_T string the C API takes
// for paths, which is UTF-8 outside Windows. The release func frees it.
func cPath(path string) (unsafe.Pointer, func(), error) {
	p := unsafe.Pointer(C.CString(path))
	return p, func() { C.free(p) }, nil
}
