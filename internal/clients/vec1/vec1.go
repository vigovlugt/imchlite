// Package vec1 embeds the vec1 SQLite vector-search extension. The shared
// library is extracted to the user cache directory on first use, where it
// persists between runs.
package vec1

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/vigovlugt/imchlite/internal/cachedir"
)

// Setup returns the directory holding the extracted vec1 extension, writing
// the embedded copy there on first use. The directory persists between runs.
func Setup() (string, error) {
	if len(libraryBytes) == 0 {
		return "", fmt.Errorf("no embedded vec1 extension for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	return cachedir.Ensure("vec1", func(dir string) error {
		return os.WriteFile(filepath.Join(dir, libraryFilename), libraryBytes, 0o755)
	})
}

// LibraryPath returns the path of the extracted extension library, for use as
// the argument of sqlite3's load_extension or a driver connect hook.
func LibraryPath(dir string) string {
	return filepath.Join(dir, libraryFilename)
}
