//go:build windows && amd64

package vec1

import _ "embed"

//go:embed bin/windows/vec1.dll
var libraryBytes []byte

const libraryFilename = "vec1.dll"
