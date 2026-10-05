//go:build linux && amd64

package onnxruntime

import _ "embed"

//go:embed bin/linux/libonnxruntime.so
var libraryBytes []byte

const libraryFilename = "libonnxruntime.so"
