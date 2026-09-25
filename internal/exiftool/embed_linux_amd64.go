//go:build linux && amd64

package exiftool

import (
	"embed"
)

//go:embed all:bin/linux
var files embed.FS

const filesRoot = "bin/linux"

// extractedFilename is the name of the exiftool entry point within the
// extracted directory.
const extractedFilename = "exiftool"
