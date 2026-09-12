package main

// File mirrors the file table in migration001.
type File struct {
	ID int64

	// the logical content this file holds a copy of; null until the
	// asset's checksum has been computed
	AssetID *int64

	// filesystem identity (path is relative to the library root)
	Path    string
	Inode   int64
	Size    int64
	MtimeS  int64
	MtimeNs int64

	// true once the file is no longer reachable on disk
	IsOffline bool

	CreatedAt int64
	UpdatedAt int64
}
