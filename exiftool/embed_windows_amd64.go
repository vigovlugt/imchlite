//go:build windows && amd64

package exiftool

import (
	"embed"
)

//go:embed all:bin/windows
var files embed.FS

const filesRoot = "bin/windows"

// extractedFilename is the name of the exiftool entry point within the
// extracted directory.
const extractedFilename = "exiftool.exe"
