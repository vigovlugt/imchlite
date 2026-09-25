//go:build windows && amd64

package onnxruntime

import _ "embed"

//go:embed bin/windows/onnxruntime.dll
var libraryBytes []byte

const libraryFilename = "onnxruntime.dll"
