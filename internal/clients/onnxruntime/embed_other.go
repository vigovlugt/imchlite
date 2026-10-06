//go:build !(linux && amd64) && !(windows && amd64)

package onnxruntime

import "embed"

var binFS embed.FS

const (
	binDir          = ""
	libraryFilename = "libonnxruntime.so"
	webGPUFilename  = "libonnxruntime_providers_webgpu.so"
)
