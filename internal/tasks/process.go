// Package tasks processes the library: the queue the workers drain, and one
// file per task type (index, file, metadata, thumbnail and clip).
package tasks

import (
	"context"
	"log"
	"time"

	"github.com/vigovlugt/imchlite/internal/ai"
	exiftoolbin "github.com/vigovlugt/imchlite/internal/clients/exiftool"
	"github.com/vigovlugt/imchlite/internal/clients/ffmpeg"
	"github.com/vigovlugt/imchlite/internal/disk"
	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/queue"
	"github.com/vigovlugt/imchlite/internal/repository"
	"github.com/vigovlugt/imchlite/internal/utils"
)

// Task priorities. Higher values are processed first; tasks of equal
// priority keep FIFO order. Indexing runs before anything else so the
// library walk is not competing with processing for disk I/O. Clip tasks
// run before the next file, so a thumbnail that was just written is still
// in the page cache. A file task that creates an asset runs its thumbnail
// task inline, reusing the bytes it read, so thumbnail tasks are only
// queued when recovering work from a previous run; they use the lowest
// priority so new files are linked first.
const (
	indexPriority     = 3
	clipPriority      = 2
	filePriority      = 1
	thumbnailPriority = 0
)

// NewQueue creates the queue the indexer and processor feed and the workers
// drain. It holds any task type; the processor switches on the concrete
// type.
func NewQueue() *queue.Queue[any] {
	return queue.New[any]()
}

// processor holds the shared dependencies of the workers.
type processor struct {
	ctx        context.Context
	libraryDir string
	dataDir    string
	ffmpeg     *ffmpeg.FFmpeg
	disk       *disk.Disk
	files      *repository.File
	assets     *repository.Asset
	clip       *ai.ClipVisual
	// excludes are the library paths the indexer skips.
	excludes utils.Excludes
	// retryFailed makes failed steps run again, like pending ones.
	retryFailed bool
}

// NewProcessor creates a processor sharing the given repositories, the
// extracted ffmpeg binary, the library's disk lock and the clip model. With
// retryFailed, steps that failed in a previous run are run again. Paths
// matching excludes are not indexed.
func NewProcessor(ctx context.Context, libraryDir, dataDir string, excludes utils.Excludes, ff *ffmpeg.FFmpeg, d *disk.Disk, files *repository.File, assets *repository.Asset, clip *ai.ClipVisual, retryFailed bool) *processor {
	return &processor{
		ctx:         ctx,
		libraryDir:  libraryDir,
		dataDir:     dataDir,
		ffmpeg:      ff,
		disk:        d,
		files:       files,
		assets:      assets,
		clip:        clip,
		excludes:    excludes,
		retryFailed: retryFailed,
	}
}

// Worker consumes tasks from the queue until it is closed. Each worker runs
// its own exiftool process.
func (p *processor) Worker(et *exiftoolbin.Exiftool, q *queue.Queue[any], state *TaskState) {
	for {
		t, ok := q.Pop()
		if !ok {
			break
		}
		p.run(t, et, q, state)
		q.Done()
	}
	log.Printf("worker finished")
}

// run processes a single task popped from q.
func (p *processor) run(t any, et *exiftoolbin.Exiftool, q *queue.Queue[any], state *TaskState) {
	if p.ctx.Err() != nil {
		// Shutting down: drain the queue without touching disk.
		if _, ok := t.(fileTask); ok {
			state.errored.Add(1)
		}
		return
	}
	switch task := t.(type) {
	case indexTask:
		log.Printf("indexing library %s", p.libraryDir)
		if err := IndexLibrary(p.ctx, p.libraryDir, p.dataDir, p.excludes, p.disk, p.files, p.assets, q, state, p.retryFailed); err != nil {
			log.Printf("indexing failed: %v", err)
			return
		}
		log.Printf("indexing completed")
	case fileTask:
		if err := p.processFile(task, et, q); err != nil {
			log.Printf("process file=%d path=%s: %v", task.FileID, task.Path, err)
			state.errored.Add(1)
			return
		}
		state.processed.Add(1)
	case thumbnailTask:
		if err := p.processThumbnail(task.Asset, "", nil, false, q); err != nil {
			log.Printf("thumbnail asset=%d: %v", task.Asset.ID, err)
		}
	case clipTask:
		if err := p.processClip(task.Asset, task.ThumbnailWarm); err != nil {
			log.Printf("clip asset=%d: %v", task.Asset.ID, err)
		}
	default:
		log.Printf("unknown task type %T", t)
	}
}

// readDisk runs fn, which reads from the library's disk, under the disk
// lock unless warm reports that the bytes fn reads are in the page cache.
// what describes the read for the disk's watch log. It returns how long it
// waited for the lock.
func (p *processor) readDisk(warm bool, what string, fn func() error) (time.Duration, error) {
	if warm {
		return 0, fn()
	}
	return p.disk.Do(p.ctx, what, fn)
}

// shouldRun reports whether a step with the given status needs to run.
func (p *processor) shouldRun(status entity.TaskStatus) bool {
	return shouldRun(status, p.retryFailed)
}

// shouldRun reports whether a step with the given status needs to run,
// with retryFailed making failed steps run again.
func shouldRun(status entity.TaskStatus, retryFailed bool) bool {
	return status == entity.TaskStatusPending || (retryFailed && status == entity.TaskStatusFailed)
}

// EnqueuePendingAssetTasks re-adds the thumbnail and clip tasks of assets
// with a pending step, e.g. because the process restarted mid-task, and
// with retryFailed also of assets with a failed step. An asset whose
// thumbnail needs to run gets only a thumbnail task, which enqueues its
// clip task when done. It returns the number of tasks enqueued.
func EnqueuePendingAssetTasks(ctx context.Context, assets *repository.Asset, q *queue.Queue[any], retryFailed bool) (int, error) {
	pending, err := assets.GetAssetsWithPendingTasks(ctx, retryFailed)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range pending {
		switch {
		case shouldRun(a.ThumbnailStatus, retryFailed):
			q.Push(thumbnailTask{Asset: a}, thumbnailPriority)
		case shouldRun(a.ClipStatus, retryFailed):
			q.Push(clipTask{Asset: a}, clipPriority)
		default:
			continue
		}
		n++
	}
	return n, nil
}
