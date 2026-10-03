package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/vigovlugt/imchlite/internal/ai"
	"github.com/vigovlugt/imchlite/internal/disk"
	"github.com/vigovlugt/imchlite/internal/entity"
	exiftoolbin "github.com/vigovlugt/imchlite/internal/exiftool"
	"github.com/vigovlugt/imchlite/internal/ffmpeg"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/queue"
	"github.com/vigovlugt/imchlite/internal/repository"
	"github.com/vigovlugt/imchlite/internal/utils"
)

// fileTask checksums a file and links it to its (possibly new) asset.
type fileTask struct {
	FileID int64
	Path   string
}

// assetTask runs the asset's pending processing steps (thumbnail, then
// clip). Each step's outcome is stored in its own status column, so a task
// re-enqueued at startup skips the steps that already finished.
type assetTask struct {
	Asset entity.Asset
}

// indexTask walks the library, enqueueing asset tasks for new and changed
// files.
type indexTask struct{}

// Task priorities. Higher values are processed first; tasks of equal
// priority keep FIFO order. Indexing runs before anything else so the
// library walk is not competing with processing for disk I/O. Asset tasks
// are only queued when recovering work from a previous run; a file task
// that creates an asset runs its asset task inline instead. They use the
// lowest priority so new files are linked first and recovered work only
// consumes idle worker capacity.
const (
	indexPriority = 1
	filePriority  = 0
	assetPriority = -1
)

// EnqueueIndexTask schedules a walk of the library with the highest
// priority.
func EnqueueIndexTask(q *queue.Queue[any]) {
	q.Push(indexTask{}, indexPriority)
}

// NewQueue creates the queue the indexer and processor feed and the workers
// drain. It holds any task type; the processor switches on the concrete
// type.
func NewQueue() *queue.Queue[any] {
	return queue.New[any]()
}

const (
	thumbnailSize    = 256
	thumbnailQuality = 80
	// maxInMemoryImageSize bounds how large a pipeable image may be before
	// it is streamed from disk instead of buffered in memory.
	maxInMemoryImageSize = 256 << 20
	copyBufferSize       = 1 << 20
	// maxWarmFileSize bounds how large a file may be for its bytes to be
	// assumed still in the OS page cache right after it was checksummed, so
	// that exiftool and ffmpeg reading it again skip the disk lock. Parts of
	// larger files (long videos) may already be evicted by then.
	maxWarmFileSize = 256 << 20
)

// pipeableImageExtensions are image formats ffmpeg can demux from a
// non-seekable stdin stream, allowing thumbnail generation from the bytes
// already read for checksumming.
var pipeableImageExtensions = map[string]struct{}{
	".jpeg": {}, ".jpg": {}, ".jpe": {}, ".png": {}, ".webp": {}, ".bmp": {}, ".gif": {},
}

// processor holds the shared dependencies of the asset and clip workers.
type processor struct {
	ctx             context.Context
	libraryLocation string
	ffmpeg          *ffmpeg.FFmpeg
	disk            *disk.Disk
	files           *repository.File
	assets          *repository.Asset
	clip            *ai.ClipVisual
	// retryFailed makes failed steps run again, like pending ones.
	retryFailed bool
}

// NewProcessor creates a processor sharing the given repositories, the
// extracted ffmpeg binary, the library's disk lock and the clip model. With
// retryFailed, steps that failed in a previous run are run again.
func NewProcessor(ctx context.Context, libraryLocation string, ff *ffmpeg.FFmpeg, d *disk.Disk, files *repository.File, assets *repository.Asset, clip *ai.ClipVisual, retryFailed bool) *processor {
	return &processor{
		ctx:             ctx,
		libraryLocation: libraryLocation,
		ffmpeg:          ff,
		disk:            d,
		files:           files,
		assets:          assets,
		clip:            clip,
		retryFailed:     retryFailed,
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
		if p.ctx.Err() != nil {
			// Shutting down: drain the queue without touching disk.
			if _, ok := t.(fileTask); ok {
				state.errored.Add(1)
			}
			continue
		}
		switch task := t.(type) {
		case indexTask:
			log.Printf("indexing library %s", p.libraryLocation)
			if err := IndexLibrary(p.ctx, p.libraryLocation, p.disk, p.files, q, state); err != nil {
				log.Printf("indexing failed: %v", err)
				continue
			}
			log.Printf("indexing completed")
		case fileTask:
			if err := p.processFile(task, et); err != nil {
				log.Printf("process file=%d path=%s: %v", task.FileID, task.Path, err)
				state.errored.Add(1)
				continue
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
	log.Printf("worker finished")
}

// processTimings holds the wall-clock duration in milliseconds of each
// pipeline stage for a single processed asset.
type processTimings struct {
	readMs     int64
	checksumMs int64
	metadataMs int64
	geoMs      int64
	dbMs       int64
	// diskWaitMs is the time spent waiting for the disk lock, excluded
	// from the other stages.
	diskWaitMs int64
}

// processFile checksums a file, stores (or reuses) its asset, and links the
// file row to the asset. An asset row existing implies its metadata was
// already extracted. When this task created the asset, the asset task
// follows immediately, reusing the bytes already read for checksumming.
//
// The file is read in full under the disk lock. Afterwards its bytes are in
// the OS page cache, so unless the file is too large for that to be
// assumed, the later exiftool and ffmpeg reads run without the lock.
func (p *processor) processFile(task fileTask, et *exiftoolbin.Exiftool) error {
	started := time.Now()

	absolutePath := media.ResolveLibraryPath(p.libraryLocation, task.Path)

	var (
		info     os.FileInfo
		data     []byte
		checksum []byte
		timings  processTimings
	)
	waited, err := p.disk.Do(p.ctx, func() error {
		var err error
		if info, err = os.Stat(absolutePath); err != nil {
			return fmt.Errorf("stat %s: %w", absolutePath, err)
		}

		// Images readable as a stream are read from disk exactly once: the
		// same bytes feed both the checksum and the thumbnail generation.
		if ext, ok := media.LowerExtension(task.Path); ok {
			if _, pipeable := pipeableImageExtensions[ext]; pipeable && info.Size() <= maxInMemoryImageSize {
				readStart := time.Now()
				if data, err = os.ReadFile(absolutePath); err != nil {
					return fmt.Errorf("read %s: %w", absolutePath, err)
				}
				timings.readMs = time.Since(readStart).Milliseconds()
			}
		}

		checksumStart := time.Now()
		if data != nil {
			sum := sha256.Sum256(data)
			checksum = sum[:]
		} else {
			checksum, err = fileChecksum(absolutePath)
		}
		timings.checksumMs = time.Since(checksumStart).Milliseconds()
		if err != nil {
			return fmt.Errorf("checksum %s: %w", absolutePath, err)
		}
		return nil
	})
	timings.diskWaitMs += waited.Milliseconds()
	if err != nil {
		return err
	}
	warm := info.Size() <= maxWarmFileSize

	dbStart := time.Now()
	asset, err := p.assets.GetByChecksum(p.ctx, checksum)
	timings.dbMs += time.Since(dbStart).Milliseconds()
	if err != nil {
		return fmt.Errorf("lookup asset by checksum: %w", err)
	}

	created := false
	if asset == nil {
		if asset, created, timings, err = p.createAsset(task, et, absolutePath, checksum, info, warm, timings); err != nil {
			return err
		}
	}

	dbStart = time.Now()
	if err := p.files.LinkAsset(p.ctx, task.FileID, asset.ID); err != nil {
		return fmt.Errorf("link file %d to asset %d: %w", task.FileID, asset.ID, err)
	}
	timings.dbMs += time.Since(dbStart).Milliseconds()

	log.Printf(
		"processed file=%d path=%s asset=%d total_ms=%d disk_wait_ms=%d read_ms=%d checksum_ms=%d metadata_ms=%d geo_ms=%d db_ms=%d",
		task.FileID, task.Path, asset.ID,
		time.Since(started).Milliseconds(),
		timings.diskWaitMs, timings.readMs, timings.checksumMs, timings.metadataMs, timings.geoMs, timings.dbMs,
	)

	if created {
		// The file is linked, so a failing asset step does not fail the file:
		// it stays pending or failed in its own status column.
		if err := p.processAsset(*asset, task.Path, data, warm); err != nil {
			log.Printf("process asset=%d: %v", asset.ID, err)
		}
	}
	return nil
}

// readDisk runs fn, which reads from the library's disk, under the disk
// lock unless warm reports that the bytes fn reads are in the page cache.
// It returns how long it waited for the lock.
func (p *processor) readDisk(warm bool, fn func() error) (time.Duration, error) {
	if warm {
		return 0, fn()
	}
	return p.disk.Do(p.ctx, fn)
}

// shouldRun reports whether a step with the given status needs to run.
func (p *processor) shouldRun(status entity.TaskStatus) bool {
	return status == entity.TaskStatusPending || (p.retryFailed && status == entity.TaskStatusFailed)
}

// processAsset runs the asset's pending steps in order, skipping the ones
// whose status is already ok, or failed unless failed steps are retried. path and data are the file the
// asset was just created from and its bytes when already read; with an
// empty path an online file of the asset is looked up. warm reports that
// the file at path was just read and is in the page cache, so reading it
// again does not take the disk lock. A step interrupted by shutdown or a
// model that failed to load stays pending, to be retried on the next
// startup.
func (p *processor) processAsset(asset entity.Asset, path string, data []byte, warm bool) error {
	// A thumbnail created here was just written, so it is in the page cache.
	thumbnailWarm := false
	if p.shouldRun(asset.ThumbnailStatus) {
		if path == "" {
			var err error
			if path, _, err = p.assets.LiveFileForChecksum(p.ctx, asset.Checksum); err != nil {
				return fmt.Errorf("find file for asset: %w", err)
			}
		}

		started := time.Now()
		absolutePath := media.ResolveLibraryPath(p.libraryLocation, path)
		waited, err := p.readDisk(warm, func() error {
			return p.createThumbnail(asset.Checksum, absolutePath, data)
		})
		if err != nil {
			if p.ctx.Err() != nil {
				return fmt.Errorf("thumbnail for %s: %w", path, err)
			}
			log.Printf("thumbnail for %s: %v", path, err)
			asset.ThumbnailStatus = entity.TaskStatusFailed
		} else {
			asset.ThumbnailStatus = entity.TaskStatusOK
			thumbnailWarm = true
		}
		if err := p.assets.SetThumbnailStatus(p.ctx, asset.ID, asset.ThumbnailStatus); err != nil {
			return err
		}
		log.Printf("thumbnailed asset=%d path=%s total_ms=%d disk_wait_ms=%d", asset.ID, path, time.Since(started).Milliseconds(), waited.Milliseconds())
	}

	if p.shouldRun(asset.ClipStatus) {
		if asset.ThumbnailStatus == entity.TaskStatusFailed {
			// Without a thumbnail there is nothing to embed.
			if asset.ClipStatus == entity.TaskStatusFailed {
				return nil
			}
			return p.assets.SetClipStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
		}
		if err := p.processClip(asset, thumbnailWarm); err != nil {
			return err
		}
	}
	return nil
}

// processClip embeds the asset's thumbnail with the clip model and marks the
// asset's clip step ok, or failed when the thumbnail cannot be embedded.
// thumbnailWarm reports that the thumbnail was just written and is in the
// page cache, so reading it does not take the disk lock.
func (p *processor) processClip(asset entity.Asset, thumbnailWarm bool) error {
	if err := p.clip.WaitLoad(p.ctx); err != nil {
		return fmt.Errorf("clip model: %w", err)
	}

	started := time.Now()

	var thumbnail []byte
	waited, err := p.readDisk(thumbnailWarm, func() error {
		var err error
		thumbnail, err = os.ReadFile(thumbnailPath(p.libraryLocation, asset.Checksum))
		return err
	})
	if err != nil {
		if p.ctx.Err() != nil {
			return fmt.Errorf("read thumbnail: %w", err)
		}
		log.Printf("clip asset=%d: read thumbnail: %v", asset.ID, err)
		return p.assets.SetClipStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
	}

	embedding, timings, err := p.clip.Embed(p.ctx, bytes.NewReader(thumbnail))
	if err != nil {
		if p.ctx.Err() != nil {
			return fmt.Errorf("embed thumbnail: %w", err)
		}
		log.Printf("clip asset=%d: embed thumbnail: %v", asset.ID, err)
		return p.assets.SetClipStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
	}

	if err := p.assets.InsertClipEmbedding(p.ctx, asset.ID, utils.EncodeEmbedding(embedding)); err != nil {
		return err
	}

	log.Printf(
		"clipped asset=%d dim=%d total_ms=%d disk_wait_ms=%d decode_ms=%d transform_ms=%d inference_ms=%d",
		asset.ID, len(embedding),
		time.Since(started).Milliseconds(),
		waited.Milliseconds(),
		timings.DecodeMs, timings.TransformMs, timings.InferenceMs,
	)
	return nil
}

// EnqueuePendingAssetTasks re-adds asset tasks for assets with a pending
// step, e.g. because the process restarted mid-task, and with retryFailed
// also for assets with a failed step. It returns the number of tasks
// enqueued.
func EnqueuePendingAssetTasks(ctx context.Context, assets *repository.Asset, q *queue.Queue[any], retryFailed bool) (int, error) {
	pending, err := assets.GetAssetsWithPendingTasks(ctx, retryFailed)
	if err != nil {
		return 0, err
	}
	for _, a := range pending {
		q.Push(assetTask{Asset: a}, assetPriority)
	}
	return len(pending), nil
}

// createAsset probes the file's metadata and inserts its asset row with all
// processing steps pending. A concurrent worker may have stored the same
// content first; the asset is re-fetched by checksum so the row with the
// canonical id is returned, and created reports whether this call inserted
// it.
func (p *processor) createAsset(task fileTask, et *exiftoolbin.Exiftool, absolutePath string, checksum []byte, info os.FileInfo, warm bool, timings processTimings) (asset *entity.Asset, created bool, _ processTimings, _ error) {
	metadataStart := time.Now()
	var meta exiftoolbin.MediaMetadata
	waited, err := p.readDisk(warm, func() error {
		var err error
		meta, err = et.ProbeMetadata(absolutePath)
		return err
	})
	timings.metadataMs = (time.Since(metadataStart) - waited).Milliseconds()
	timings.diskWaitMs += waited.Milliseconds()
	if err != nil {
		return nil, false, timings, fmt.Errorf("probe metadata: %w", err)
	}

	mimeType := meta.MimeType
	if mimeType == "" {
		mimeType = exiftoolbin.MimeTypeFromPath(task.Path)
	}

	mtime := info.ModTime().Unix()
	asset = &entity.Asset{
		Checksum:       checksum,
		MimeType:       mimeType,
		Type:           media.TypeFromPath(task.Path),
		FileCreatedAt:  mtime,
		FileModifiedAt: mtime,
		LocalDateTime:  meta.LocalTakenAt,
		DateTime:       meta.TakenAtUTC,
		TimeZone:       meta.TimeZone,
		Latitude:       meta.Latitude,
		Longitude:      meta.Longitude,
		City:           meta.City,
		Country:        meta.Country,
		Width:          meta.Width,
		Height:         meta.Height,
		DurationMs:     meta.DurationMs,
		Orientation:    meta.Orientation,
	}

	// Sidecars are separate files, not warmed by reading the media file.
	waited, err = p.disk.Do(p.ctx, func() error {
		applySidecars(p.libraryLocation, task.Path, asset)
		return nil
	})
	timings.diskWaitMs += waited.Milliseconds()
	if err != nil {
		return nil, false, timings, err
	}

	geoStart := time.Now()
	err = applyCityCountry(asset, et)
	if err != nil {
		log.Printf("apply city country for %s: %v", task.Path, err)
	}
	timings.geoMs = time.Since(geoStart).Milliseconds()

	insertStart := time.Now()
	created, err = p.assets.Insert(p.ctx, asset)
	if err != nil {
		return nil, false, timings, fmt.Errorf("store asset for %s: %w", task.Path, err)
	}

	stored, err := p.assets.GetByChecksum(p.ctx, checksum)
	timings.dbMs += time.Since(insertStart).Milliseconds()
	if err != nil {
		return nil, false, timings, fmt.Errorf("lookup asset by checksum: %w", err)
	}

	return stored, created, timings, nil
}

func (p *processor) createThumbnail(checksum []byte, absolutePath string, data []byte) error {
	dest := thumbnailPath(p.libraryLocation, checksum)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create thumbnail dir: %w", err)
	}

	if data != nil {
		return p.ffmpeg.ThumbnailFromReader(p.ctx, bytes.NewReader(data), dest, thumbnailSize, thumbnailQuality)
	}

	return p.ffmpeg.Thumbnail(p.ctx, absolutePath, dest, thumbnailSize, thumbnailQuality)
}

// thumbnailPath returns the canonical thumbnail location for the asset with
// the given checksum.
func thumbnailPath(libraryLocation string, checksum []byte) string {
	dir := filepath.Join(libraryLocation, ".imchlite", "thumbnails")
	hex := fmt.Sprintf("%x", checksum)
	return filepath.Join(dir, hex[0:2], hex[2:4], hex+".webp")
}

// fileChecksum returns the sha256 of a file's bytes.
func fileChecksum(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, copyBufferSize)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func applyCityCountry(asset *entity.Asset, exiftool *exiftoolbin.Exiftool) error {
	if (asset.Latitude != 0 || asset.Longitude != 0) && (asset.City == "" || asset.Country == "") {
		city, country, err := exiftool.ReverseGeocode(asset.Latitude, asset.Longitude)
		if err != nil {
			return fmt.Errorf("reverse geocode: %w", err)
		}
		asset.City = city
		asset.Country = country
	}
	return nil
}
