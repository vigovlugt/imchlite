//go:build !(linux && amd64) && !(windows && amd64)

package ffmpeg

var ffmpegBytes []byte

const ffmpegFilename = "ffmpeg"
