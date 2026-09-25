package library

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/queue"
	"github.com/vigovlugt/imchlite/internal/repository"
)

// IndexerState tracks live progress of the background indexer so the api can
// report on it.
type IndexerState struct {
	startedAt  time.Time
	discovered atomic.Int64
	processed  atomic.Int64
	// skipped counts files that needed no processing in this run because
	// they were already processed by a previous run.
	skipped   atomic.Int64
	errored   atomic.Int64
	completed atomic.Bool
	failed    atomic.Bool
	errMsg    atomic.Pointer[string]
}

// NewIndexerState creates a fresh, empty indexer state.
func NewIndexerState() *IndexerState {
	return &IndexerState{startedAt: time.Now()}
}

func (s *IndexerState) complete() {
	s.completed.Store(true)
}

func (s *IndexerState) fail(err error) {
	msg := err.Error()
	s.errMsg.Store(&msg)
	s.failed.Store(true)
}

// IndexStatus is the serializable snapshot of the indexer's progress.
type IndexStatus struct {
	StartedAt  time.Time `json:"startedAt"`
	Discovered int64     `json:"discovered"`
	Processed  int64     `json:"processed"`
	Errored    int64     `json:"errored"`
	Phase      string    `json:"phase"`
	ETASeconds int64     `json:"etaSeconds,omitempty"`
	Completed  bool      `json:"completed"`
	Failed     bool      `json:"failed"`
	Error      string    `json:"error,omitempty"`
}

// Status returns the current progress snapshot.
func (s *IndexerState) Status() IndexStatus {
	status := IndexStatus{
		StartedAt:  s.startedAt,
		Discovered: s.discovered.Load(),
		Processed:  s.processed.Load(),
		Errored:    s.errored.Load(),
		Completed:  s.completed.Load(),
		Failed:     s.failed.Load(),
	}

	switch {
	case s.failed.Load():
		status.Phase = "failed"
	case !s.completed.Load():
		status.Phase = "indexing"
	case s.processed.Load()+s.errored.Load() < s.discovered.Load():
		status.Phase = "processing"
	default:
		status.Phase = "completed"
	}

	// The ETA is based only on work performed in this run: skipped files
	// were counted at walk speed (near-zero time), so including them in
	// the rate would understate the remaining time.
	done := status.Processed - int64(s.skipped.Load()) + status.Errored
	remaining := status.Discovered - status.Processed - status.Errored
	if status.Phase != "failed" && done > 0 && remaining > 0 {
		elapsed := time.Since(s.startedAt)
		status.ETASeconds = int64(elapsed / time.Duration(done) * time.Duration(remaining) / time.Second)
	}

	if msg := s.errMsg.Load(); msg != nil {
		status.Error = *msg
	}
	return status
}

// statInfo is the comparable filesystem identity of a file.
type statInfo struct {
	inode   int64
	size    int64
	mtimeS  int64
	mtimeNs int64
}

func fileStat(f entity.File) statInfo {
	return statInfo{inode: f.Inode, size: f.Size, mtimeS: f.MtimeS, mtimeNs: f.MtimeNs}
}

// IndexLibrary walks the library and records the run's outcome in the given
// indexer state.
func IndexLibrary(ctx context.Context, libraryLocation string, fileRepo *repository.File, queue *queue.Queue[assetTask], state *IndexerState) error {
	if err := walkLibrary(ctx, libraryLocation, fileRepo, queue, state); err != nil {
		state.fail(err)
		return err
	}
	state.complete()
	return nil
}

func walkLibrary(ctx context.Context, libraryLocation string, fileRepo *repository.File, queue *queue.Queue[assetTask], state *IndexerState) error {
	existingFiles, err := fileRepo.GetAll(ctx)
	if err != nil {
		return fmt.Errorf("snapshot files: %w", err)
	}

	fileByPath := make(map[string]entity.File, len(existingFiles))
	fileByInode := make(map[int64]entity.File, len(existingFiles))
	for _, file := range existingFiles {
		fileByPath[file.Path] = file
		fileByInode[file.Inode] = file
	}
	log.Printf("snapshot: %d known files, walking library", len(existingFiles))

	seenPaths := map[string]struct{}{}

	err = filepath.WalkDir(libraryLocation, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}

		if err := ctx.Err(); err != nil {
			return err
		}

		if d.Name()[0] == '.' {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if d.Name() == "@eaDir" || d.Name() == "#recycle" || d.Name() == "#snapshot" || d.Name() == "System Volume Information" || d.Name() == "$RECYCLE.BIN" {
				return filepath.SkipDir
			}
			return nil
		}

		if !media.IsMediaPath(path) {
			return nil
		}

		state.discovered.Add(1)

		info, err := d.Info()
		if err != nil {
			log.Printf("indexer: stat failed for %s: %v", path, err)
			return nil
		}

		relativePath, err := filepath.Rel(libraryLocation, path)
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}
		// Paths are stored and keyed with forward slashes on every platform;
		// ResolveLibraryPath converts back to OS-native form at I/O boundaries.
		relativePath = filepath.ToSlash(relativePath)
		seenPaths[relativePath] = struct{}{}

		mtime := info.ModTime()
		stat := statInfo{size: info.Size(), mtimeS: mtime.Unix(), mtimeNs: int64(mtime.Nanosecond())}

		inode, err := media.FileInode(d, path)
		if err != nil {
			log.Printf("indexer: inode lookup failed for %s: %v", path, err)
			return nil
		}
		stat.inode = inode

		if existing, ok := fileByPath[relativePath]; ok && fileStat(existing) == stat && !existing.IsOffline {
			if existing.AssetID == nil {
				queue.Push(assetTask{FileID: existing.ID, Path: relativePath}, assetPriority)
			} else {
				state.skipped.Add(1)
				state.processed.Add(1)
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

		knownFile := repository.NewFile{
			AssetID:   assetID,
			Path:      relativePath,
			Inode:     inode,
			Size:      stat.size,
			MtimeS:    stat.mtimeS,
			MtimeNs:   stat.mtimeNs,
			IsOffline: false,
		}
		fileID, err := fileRepo.Upsert(ctx, knownFile)
		if err != nil {
			return fmt.Errorf("upsert %s: %w", relativePath, err)
		}
		if assetID == nil {
			// The row has no asset yet; notify the asset worker.
			queue.Push(assetTask{FileID: fileID, Path: relativePath}, assetPriority)
		} else {
			// The moved file's content is unchanged and already has an
			// asset, so no processing is needed.
			state.skipped.Add(1)
			state.processed.Add(1)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walk library: %w", err)
	}

	// Loop over live files which are now offline
	offlineFiles := []int64{}

	for path, file := range fileByPath {
		if file.IsOffline {
			continue
		}
		if _, ok := seenPaths[path]; !ok {
			offlineFiles = append(offlineFiles, file.ID)
		}
	}

	if err := fileRepo.MarkOffline(ctx, offlineFiles); err != nil {
		return fmt.Errorf("mark offline files: %w", err)
	}
	if len(offlineFiles) > 0 {
		log.Printf("marked %d files offline", len(offlineFiles))
	}

	return nil
}
