//go:build linux && amd64

package ffmpeg

import _ "embed"

//go:embed bin/linux/ffmpeg
var ffmpegBytes []byte

const ffmpegFilename = "ffmpeg"
