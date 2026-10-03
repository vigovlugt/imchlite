//go:build windows

package media

import (
	"io/fs"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileIDBothDirInfo mirrors FILE_ID_BOTH_DIR_INFO, which x/sys/windows does
// not define.
type fileIDBothDirInfo struct {
	NextEntryOffset uint32
	FileIndex       uint32
	CreationTime    windows.Filetime
	LastAccessTime  windows.Filetime
	LastWriteTime   windows.Filetime
	ChangeTime      windows.Filetime
	EndOfFile       uint64
	AllocationSize  uint64
	FileAttributes  uint32
	FileNameLength  uint32
	EaSize          uint32
	ShortNameLength uint32
	ShortName       [12]uint16
	FileID          uint64
	FileName        [1]uint16
}

const (
	ioReparseTagDedup        = 0x80000013
	reparseTagNameSurrogate  = 0x20000000
	fileIDBothDirInfoBufSize = 64 * 1024
)

// ReadDir lists a directory with FileIdBothDirectoryInfo, which returns each
// entry's size, mtime and file ID from the NTFS directory index itself. That
// way no file has to be stat'ed or opened, which would read its MFT record.
// File systems without file IDs (FAT, exFAT) fall back to a plain listing
// without stat data.
func ReadDir(path string) ([]DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := windows.Handle(f.Fd())

	var flags uint32
	if err := windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, &flags, nil, 0); err != nil || flags&windows.FILE_SUPPORTS_OPEN_BY_FILE_ID == 0 {
		return readDirPlain(f)
	}

	// A []uint64 keeps the buffer 8-byte aligned, as the entries require.
	buf := make([]uint64, fileIDBothDirInfoBufSize/8)
	class := uint32(windows.FileIdBothDirectoryRestartInfo)
	var entries []DirEntry
	for {
		err := windows.GetFileInformationByHandleEx(h, class, (*byte)(unsafe.Pointer(&buf[0])), fileIDBothDirInfoBufSize)
		if err == windows.ERROR_NO_MORE_FILES {
			break
		}
		if class == windows.FileIdBothDirectoryRestartInfo {
			if err == windows.ERROR_FILE_NOT_FOUND {
				// An empty root directory; see os/dir_windows.go.
				break
			}
			if err == windows.ERROR_INVALID_PARAMETER || err == windows.ERROR_NOT_SUPPORTED {
				return readDirPlain(f)
			}
		}
		if err != nil {
			return nil, &fs.PathError{Op: "GetFileInformationByHandleEx", Path: path, Err: err}
		}
		class = windows.FileIdBothDirectoryInfo

		offset := uintptr(0)
		for {
			info := (*fileIDBothDirInfo)(unsafe.Add(unsafe.Pointer(&buf[0]), offset))
			name := windows.UTF16ToString(unsafe.Slice(&info.FileName[0], info.FileNameLength/2))
			if name != "." && name != ".." {
				entries = append(entries, dirEntryFromInfo(name, info))
			}
			if info.NextEntryOffset == 0 {
				break
			}
			offset += uintptr(info.NextEntryOffset)
		}
	}
	return entries, nil
}

// dirEntryFromInfo maps an entry the way os.ReadDir does: symlinks and
// junctions (name surrogates) are never directories, so the walk does not
// follow them.
func dirEntryFromInfo(name string, info *fileIDBothDirInfo) DirEntry {
	entry := DirEntry{
		Name:    name,
		Inode:   info.FileID,
		HasStat: true,
		Size:    int64(info.EndOfFile),
		ModTime: time.Unix(0, info.LastWriteTime.Nanoseconds()),
	}

	isReparse := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	// For reparse points, EaSize holds the reparse tag.
	tag := info.EaSize
	switch {
	case isReparse && tag&reparseTagNameSurrogate != 0:
		if tag == windows.IO_REPARSE_TAG_SYMLINK {
			entry.Type = fs.ModeSymlink
		} else {
			entry.Type = fs.ModeIrregular
		}
	case info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0:
		entry.Type = fs.ModeDir
	case isReparse && tag != ioReparseTagDedup:
		entry.Type = fs.ModeIrregular
	}
	return entry
}

func readDirPlain(f *os.File) ([]DirEntry, error) {
	dirEntries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	entries := make([]DirEntry, len(dirEntries))
	for i, d := range dirEntries {
		entries[i] = DirEntry{Name: d.Name(), Type: d.Type()}
	}
	return entries, nil
}
