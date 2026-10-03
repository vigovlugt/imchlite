package media

import (
	"io/fs"
	"time"
)

// DirEntry is a directory entry together with the inode number the
// directory listing reports for it, so callers can stat entries in on-disk
// order without a stat per entry first.
type DirEntry struct {
	Name string
	// Inode is 0 when the platform's directory listing does not report it.
	Inode uint64
	// Type holds only the fs.ModeType bits of the entry.
	Type fs.FileMode

	// HasStat reports whether the listing also carried Size and ModTime
	// (and a valid Inode), so the entry needs no stat of its own.
	HasStat bool
	Size    int64
	ModTime time.Time
}

func (e DirEntry) IsDir() bool {
	return e.Type.IsDir()
}
