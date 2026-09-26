//go:build linux && amd64

package vec1

import _ "embed"

//go:embed bin/linux/vec1.so
var libraryBytes []byte

const libraryFilename = "vec1.so"
