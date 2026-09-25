//go:build !(linux && amd64) && !(windows && amd64)

package onnxruntime

var libraryBytes []byte

const libraryFilename = "libonnxruntime.so"
