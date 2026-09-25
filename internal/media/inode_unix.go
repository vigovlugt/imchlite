//go:build !windows

package media

import (
	"fmt"
	"io/fs"
	"syscall"
)

// FileInode returns the inode number of a directory entry, using the stat
// result already attached to the entry's FileInfo by the OS to avoid a second
// stat syscall.
func FileInode(d fs.DirEntry, path string) (int64, error) {
	info, err := d.Info()
	if err != nil {
		return 0, err
	}

	switch stat := info.Sys().(type) {
	case *syscall.Stat_t:
		return int64(stat.Ino), nil
	}

	return 0, fmt.Errorf("fileInfo.Sys() is not a *syscall.Stat_t")
}
