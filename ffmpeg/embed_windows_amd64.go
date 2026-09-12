//go:build windows && amd64

package ffmpeg

import _ "embed"

//go:embed bin/windows/ffmpeg.exe
var ffmpegBytes []byte

const ffmpegFilename = "ffmpeg.exe"
