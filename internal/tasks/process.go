// Package tasks processes the library: the queue the workers drain, and one
// file per task type (index, file, asset and its thumbnail and clip steps).
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
// library walk is not competing with processing for disk I/O. Asset tasks
// are only queued when recovering work from a previous run, after the walk; a file task
// that creates an asset runs its asset task inline instead. They use the
// lowest priority so new files are linked first and recovered work only
// consumes idle worker capacity.
const (
	indexPriority = 1
	filePriority  = 0
	assetPriority = -1
)

// NewQueue creates the queue the indexer and processor feed and the workers
// drain. It holds any task type; the processor switches on the concrete
// type.
func NewQueue() *queue.Queue[any] {
	return queue.New[any]()
}

// processor holds the shared dependencies of the asset and clip workers.
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
func (p *processor) Worker(et *exiftoolbin.Exiftool, q *queue.Queue[any], state *IndexerState) {
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
func (p *processor) run(t any, et *exiftoolbin.Exiftool, q *queue.Queue[any], state *IndexerState) {
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
		if err := p.processFile(task, et); err != nil {
			log.Printf("process file=%d path=%s: %v", task.FileID, task.Path, err)
			state.errored.Add(1)
			return
		}
		state.processed.Add(1)
	case assetTask:
		if err := p.processAsset(task.Asset, "", nil, false); err != nil {
			log.Printf("process asset=%d: %v", task.Asset.ID, err)
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
	return status == entity.TaskStatusPending || (p.retryFailed && status == entity.TaskStatusFailed)
}
