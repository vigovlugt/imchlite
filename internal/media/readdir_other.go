//go:build !linux && !windows

package media

import "os"

// ReadDir lists a directory. Inodes are not available from the listing
// here, so every entry has Inode 0.
func ReadDir(path string) ([]DirEntry, error) {
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	entries := make([]DirEntry, len(dirEntries))
	for i, d := range dirEntries {
		entries[i] = DirEntry{Name: d.Name(), Type: d.Type()}
	}
	return entries, nil
}
