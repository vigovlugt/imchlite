//go:build !(linux && amd64) && !(windows && amd64)

package exiftool

import (
	"embed"
)

var files embed.FS

const filesRoot = ""

const extractedFilename = "exiftool"
