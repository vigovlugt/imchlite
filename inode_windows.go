//go:build windows

package main

import (
	"io/fs"

	"golang.org/x/sys/windows"
)

// fileInode returns the file ID of a directory entry. Windows' FileInfo.Sys
// holds a *syscall.Win32FileAttributeData, which carries no file index, so
// the ID has to be retrieved through the file handle. NTFS assigns IDs that
// behave like inodes for deduplication purposes; FAT has no stable file IDs
// and returns 0.
func fileInode(d fs.DirEntry, path string) (int64, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	handle, err := windows.CreateFile(
		pathUTF16,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return 0, err
	}

	return int64(uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)), nil
}
