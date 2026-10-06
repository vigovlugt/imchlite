//go:build linux && amd64

package onnxruntime

import "embed"

//go:embed bin/linux
var binFS embed.FS

const (
	binDir          = "bin/linux"
	libraryFilename = "libonnxruntime.so"
	webGPUFilename  = "libonnxruntime_providers_webgpu.so"
)
