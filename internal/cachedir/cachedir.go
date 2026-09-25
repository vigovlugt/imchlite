// Package cachedir manages the persistent on-disk cache location for the
// embedded binaries. Directories are populated atomically: content is written
// to a staging directory next to the target and renamed into place, so a
// partially written directory is never visible to other runs.
package cachedir

import (
	"fmt"
	"os"
	"path/filepath"
)

// Ensure returns the cache directory named name, creating and populating it
// if it does not exist yet. populate fills a freshly created staging
// directory; on success the staging directory is renamed to its final
// location. When several processes race, the first one to rename wins and
// the others reuse the winner's directory.
func Ensure(name string, populate func(dir string) error) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	base := filepath.Join(cacheRoot, "imchlite")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}
	target := filepath.Join(base, name)

	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return target, nil
	}

	// Staged inside the cache root so the final rename stays on one
	// filesystem and is therefore atomic.
	staging, err := os.MkdirTemp(base, "."+name+"-staging-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := populate(staging); err != nil {
		return "", fmt.Errorf("populate %s: %w", name, err)
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return "", fmt.Errorf("chmod %s: %w", staging, err)
	}
	if err := os.Rename(staging, target); err != nil {
		if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
			return target, nil
		}
		return "", fmt.Errorf("move %s into cache: %w", name, err)
	}
	return target, nil
}
