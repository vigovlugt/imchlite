package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"

	"golang.design/x/chann"
)

// statInfo is the comparable filesystem identity of a file.
type statInfo struct {
	inode   int64
	size    int64
	mtimeS  int64
	mtimeNs int64
}

func fileStat(f File) statInfo {
	return statInfo{inode: f.Inode, size: f.Size, mtimeS: f.MtimeS, mtimeNs: f.MtimeNs}
}

func indexLibrary(ctx context.Context, libraryLocation string, fileRepo *fileRepository, assetRepo *assetRepository, queue *chann.Chann[assetTask]) error {
	existingFiles, err := fileRepo.getAll(ctx)
	if err != nil {
		return fmt.Errorf("snapshot files: %w", err)
	}

	fileByPath := make(map[string]File, len(existingFiles))
	fileByInode := make(map[int64]File, len(existingFiles))
	for _, file := range existingFiles {
		fileByPath[file.Path] = file
		fileByInode[file.Inode] = file
	}

	seenPaths := map[string]struct{}{}

	err = filepath.WalkDir(libraryLocation, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if d.IsDir() {
			if d.Name() == ".imchlite" {
				return filepath.SkipDir
			}
			return nil
		}

		log.Printf("indexing %s", path)

		if !isMediaPath(path) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			log.Printf("indexer: stat failed for %s: %v", path, err)
			return nil
		}

		relativePath, err := filepath.Rel(libraryLocation, path)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}
		seenPaths[relativePath] = struct{}{}

		mtime := info.ModTime()
		stat := statInfo{size: info.Size(), mtimeS: mtime.Unix(), mtimeNs: int64(mtime.Nanosecond())}

		inode, err := fileInode(d, path)
		if err != nil {
			log.Printf("indexer: inode lookup failed for %s: %v", path, err)
			return nil
		}
		stat.inode = inode

		if existing, ok := fileByPath[relativePath]; ok && fileStat(existing) == stat && !existing.IsOffline {
			if existing.AssetID == nil {
				queue.In() <- assetTask{FileID: existing.ID, Path: relativePath}
			}
			return nil
		}

		// Reuse the asset link when this inode already holds known,
		// unchanged content (the file was moved); otherwise the file is
		// new or modified and checksumming comes later.
		var assetID *int64
		if known, ok := fileByInode[inode]; ok && fileStat(known) == stat {
			assetID = known.AssetID
		}

		knownFile := NewFile{
			AssetID:   assetID,
			Path:      relativePath,
			Inode:     inode,
			Size:      stat.size,
			MtimeS:    stat.mtimeS,
			MtimeNs:   stat.mtimeNs,
			IsOffline: false,
		}
		fileID, err := fileRepo.upsert(ctx, knownFile)
		if err != nil {
			return fmt.Errorf("upsert %s: %w", relativePath, err)
		}
		if assetID == nil {
			// The row has no asset yet; notify the asset worker.
			queue.In() <- assetTask{FileID: fileID, Path: relativePath}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walk library: %w", err)
	}

	// Loop over live files which are now offline
	offlineFiles := []int64{}

	for path, file := range fileByPath {
		if _, ok := seenPaths[path]; !ok {
			offlineFiles = append(offlineFiles, file.ID)
		}
	}

	if err := fileRepo.markOffline(ctx, offlineFiles); err != nil {
		return fmt.Errorf("mark offline files: %w", err)
	}
	if len(offlineFiles) > 0 {
		log.Printf("marked %d files offline", len(offlineFiles))
	}

	return nil
}
