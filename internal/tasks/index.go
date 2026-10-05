package tasks

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vigovlugt/imchlite/internal/disk"
	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/queue"
	"github.com/vigovlugt/imchlite/internal/repository"
	"github.com/vigovlugt/imchlite/internal/utils"
)

// indexTask walks the library, enqueueing file tasks for new and changed
// files.
type indexTask struct{}

// EnqueueIndexTask schedules a walk of the library with the highest
// priority.
func EnqueueIndexTask(q *queue.Queue[any]) {
	q.Push(indexTask{}, indexPriority)
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

// slowWalkOp is the duration above which a single walk operation (reading
// a directory, a stat, a batch upsert) is logged individually.
const slowWalkOp = 200 * time.Millisecond

// walkTimings accumulates where the walk spends its wall time between
// progress log lines, to find the cause of stalls.
type walkTimings struct {
	since   time.Time
	readDir time.Duration
	stat    time.Duration
	upsert  time.Duration
	dirs    int
	upserts int
}

func (t *walkTimings) report(files int64) {
	wall := time.Since(t.since)
	other := wall - t.readDir - t.stat - t.upsert
	log.Printf("indexer: %d files indexed (last batch %s: readdir %s over %d dirs, stat %s, upsert %s over %d batches, other %s)",
		files, wall.Round(time.Millisecond), t.readDir.Round(time.Millisecond), t.dirs,
		t.stat.Round(time.Millisecond), t.upsert.Round(time.Millisecond), t.upserts, other.Round(time.Millisecond))
	*t = walkTimings{since: time.Now()}
}

// upsertBatchSize is how many new or changed files the walk upserts per
// transaction.
const upsertBatchSize = 1000

// IndexLibrary walks the library and records the run's outcome in the given
// task state. The walk holds the disk lock throughout, so processing of
// the files it enqueues waits for it instead of making the disk seek
// between directory reads and file reads. The data dir is skipped when it
// lies inside the library, as are paths matching excludes.
//
// After the walk, thumbnail and clip tasks lost by a previous restart are re-enqueued
// (see EnqueuePendingAssetTasks), so they see which files the walk found
// online. This still runs under the disk lock: no file task can have
// created an asset yet, so none is enqueued twice while being processed
// inline.
func IndexLibrary(ctx context.Context, libraryDir, dataDir string, excludes utils.Excludes, d *disk.Disk, fileRepo *repository.File, assetRepo *repository.Asset, queue *queue.Queue[any], state *TaskState, retryFailed bool) error {
	_, err := d.Do(ctx, "index library", func() error {
		if err := walkLibrary(ctx, libraryDir, dataDir, excludes, fileRepo, queue, state); err != nil {
			return err
		}
		// Thumbnail and clip tasks live only in memory; the per-step status columns are
		// the durable marker.
		n, err := EnqueuePendingAssetTasks(ctx, assetRepo, queue, retryFailed)
		if err != nil {
			return fmt.Errorf("recover pending asset tasks: %w", err)
		}
		if n > 0 {
			log.Printf("re-enqueued %d pending asset tasks", n)
		}
		return nil
	})
	if err != nil {
		state.fail(err)
		return err
	}
	state.Complete()
	return nil
}

func walkLibrary(ctx context.Context, libraryDir, dataDir string, excludes utils.Excludes, fileRepo *repository.File, queue *queue.Queue[any], state *TaskState) error {
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

	timings := walkTimings{since: time.Now()}

	// New and changed files are upserted in batches: one transaction per
	// file would make the walk wait on a commit for every file. Their
	// tasks are pushed only once the batch is committed, so no worker sees
	// a row id that is not yet visible.
	pending := make([]repository.NewFile, 0, upsertBatchSize)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		start := time.Now()
		ids, err := fileRepo.UpsertMany(ctx, pending)
		took := time.Since(start)
		timings.upsert += took
		timings.upserts++
		if took > slowWalkOp {
			log.Printf("indexer: slow upsert of %d files: %s", len(pending), took.Round(time.Millisecond))
		}
		if err != nil {
			return err
		}
		for i, f := range pending {
			if f.AssetID == nil {
				// The row has no asset yet; enqueue a file task.
				queue.Push(fileTask{FileID: ids[i], Path: f.Path}, filePriority)
			} else {
				// The moved file's content is unchanged and already has an
				// asset, so no processing is needed.
				state.skipped.Add(1)
				state.processed.Add(1)
			}
		}
		pending = pending[:0]
		return nil
	}

	// statEntry returns the file's identity, from the directory listing
	// when it carries one (Windows) and otherwise from a stat.
	statEntry := func(path string, entry media.DirEntry) (statInfo, error) {
		if entry.HasStat {
			return statInfo{
				inode:   int64(entry.Inode),
				size:    entry.Size,
				mtimeS:  entry.ModTime.Unix(),
				mtimeNs: int64(entry.ModTime.Nanosecond()),
			}, nil
		}

		info, err := os.Lstat(path)
		if err != nil {
			return statInfo{}, fmt.Errorf("stat: %w", err)
		}
		inode, err := media.FileInode(fs.FileInfoToDirEntry(info), path)
		if err != nil {
			return statInfo{}, fmt.Errorf("inode lookup: %w", err)
		}
		mtime := info.ModTime()
		return statInfo{inode: inode, size: info.Size(), mtimeS: mtime.Unix(), mtimeNs: int64(mtime.Nanosecond())}, nil
	}

	// relativize returns path relative to the library, slash-separated.
	relativize := func(path string) (string, error) {
		relativePath, err := filepath.Rel(libraryDir, path)
		if err != nil {
			return "", fmt.Errorf("relativize %s: %w", path, err)
		}
		// Paths are stored and keyed with forward slashes on every platform;
		// ResolveLibraryPath converts back to OS-native form at I/O boundaries.
		return filepath.ToSlash(relativePath), nil
	}

	visitFile := func(path string, entry media.DirEntry) error {
		if !media.IsMediaPath(path) {
			return nil
		}

		relativePath, err := relativize(path)
		if err != nil {
			return err
		}
		if excludes.Match(relativePath) {
			return nil
		}

		if n := state.discovered.Add(1); n%1000 == 0 {
			timings.report(n)
		}

		statStart := time.Now()
		stat, err := statEntry(path, entry)
		if took := time.Since(statStart); took > slowWalkOp {
			log.Printf("indexer: slow stat of %s: %s", path, took.Round(time.Millisecond))
		}
		timings.stat += time.Since(statStart)
		if err != nil {
			log.Printf("indexer: %s: %v", path, err)
			return nil
		}
		inode := stat.inode

		seenPaths[relativePath] = struct{}{}

		if existing, ok := fileByPath[relativePath]; ok && fileStat(existing) == stat && !existing.IsOffline {
			if existing.AssetID == nil {
				queue.Push(fileTask{FileID: existing.ID, Path: relativePath}, filePriority)
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

		pending = append(pending, repository.NewFile{
			AssetID:   assetID,
			Path:      relativePath,
			Inode:     inode,
			Size:      stat.size,
			MtimeS:    stat.mtimeS,
			MtimeNs:   stat.mtimeNs,
			IsOffline: false,
		})
		if len(pending) >= upsertBatchSize {
			return flush()
		}
		return nil
	}

	var walkDir func(dir string) error
	walkDir = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		readStart := time.Now()
		entries, err := media.ReadDir(dir)
		took := time.Since(readStart)
		timings.readDir += took
		timings.dirs++
		if took > slowWalkOp {
			log.Printf("indexer: slow readdir of %s: %s", dir, took.Round(time.Millisecond))
		}
		if err != nil {
			return fmt.Errorf("walk %s: %w", dir, err)
		}

		// Stat files in inode order rather than name order: on NTFS the
		// inode is the MFT record number, so this reads file records
		// front to back instead of seeking between them on spinning disks.
		// On Windows the listing already carries each file's stat data, so
		// the order does not matter there.
		slices.SortFunc(entries, func(a, b media.DirEntry) int {
			return cmp.Compare(a.Inode, b.Inode)
		})

		var subdirs []string
		for _, e := range entries {
			if e.Name[0] == '.' {
				continue
			}
			path := filepath.Join(dir, e.Name)
			if e.IsDir() {
				if path == dataDir || e.Name == "@eaDir" || e.Name == "#recycle" || e.Name == "#snapshot" || e.Name == "System Volume Information" || e.Name == "$RECYCLE.BIN" {
					continue
				}
				relativePath, err := relativize(path)
				if err != nil {
					return err
				}
				if excludes.Match(relativePath) {
					continue
				}
				subdirs = append(subdirs, path)
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visitFile(path, e); err != nil {
				return err
			}
		}

		slices.Sort(subdirs)
		for _, subdir := range subdirs {
			if err := walkDir(subdir); err != nil {
				return err
			}
		}
		return nil
	}

	err = walkDir(libraryDir)
	if err != nil {
		return fmt.Errorf("walk library: %w", err)
	}
	if err := flush(); err != nil {
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
