//go:build windows && amd64

package onnxruntime

import "embed"

// The WebGPU plugin loads dxcompiler.dll and dxil.dll from its own directory.
//
//go:embed bin/windows
var binFS embed.FS

const (
	binDir          = "bin/windows"
	libraryFilename = "onnxruntime.dll"
	webGPUFilename  = "onnxruntime_providers_webgpu.dll"
)
