//go:build !(linux && amd64) && !(windows && amd64)

package vec1

var libraryBytes []byte

const libraryFilename = "vec1.so"
