package media

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ReadDir lists a directory with getdents64 directly, because os.ReadDir
// drops the d_ino field it returns.
func ReadDir(path string) ([]DirEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []DirEntry
	buf := make([]byte, 64*1024)
	for {
		n, err := syscall.ReadDirent(int(f.Fd()), buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return nil, &fs.PathError{Op: "getdents", Path: path, Err: err}
		}
		if n <= 0 {
			break
		}

		// struct linux_dirent64 { u64 d_ino; s64 d_off; u16 d_reclen;
		// u8 d_type; char d_name[]; }
		b := buf[:n]
		for len(b) > 0 {
			ino := binary.NativeEndian.Uint64(b[0:8])
			reclen := int(binary.NativeEndian.Uint16(b[16:18]))
			typ := b[18]
			name := b[19:reclen]
			if i := bytes.IndexByte(name, 0); i >= 0 {
				name = name[:i]
			}
			b = b[reclen:]

			if ino == 0 || string(name) == "." || string(name) == ".." {
				continue
			}

			entry := DirEntry{Name: string(name), Inode: ino}
			switch typ {
			case syscall.DT_REG:
			case syscall.DT_DIR:
				entry.Type = fs.ModeDir
			case syscall.DT_LNK:
				entry.Type = fs.ModeSymlink
			case syscall.DT_UNKNOWN:
				info, err := os.Lstat(filepath.Join(path, entry.Name))
				if err != nil {
					continue
				}
				entry.Type = info.Mode().Type()
			default:
				entry.Type = fs.ModeIrregular
			}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
