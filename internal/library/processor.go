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
	"github.com/vigovlugt/imchlite/internal/entity"
	exiftoolbin "github.com/vigovlugt/imchlite/internal/exiftool"
	"github.com/vigovlugt/imchlite/internal/ffmpeg"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/queue"
	"github.com/vigovlugt/imchlite/internal/repository"
)

type assetTask struct {
	FileID int64
	Path   string
}

type clipTask struct {
	AssetID  int64
	Checksum []byte
}

// assetPriority is the priority assigned to asset tasks. Higher values are
// processed first; tasks of equal priority keep FIFO order. Clip tasks use
// a lower priority so asset work always runs first and clip work only
// consumes idle worker capacity.
const (
	assetPriority = 0
	clipPriority  = -1
)

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
	files           *repository.File
	assets          *repository.Asset
	clip            *ai.ClipVisual
	queue           *queue.Queue[any]
}

// NewProcessor creates a processor sharing the given repositories, the
// extracted ffmpeg binary, the clip model and the task queue.
func NewProcessor(ctx context.Context, libraryLocation string, ff *ffmpeg.FFmpeg, files *repository.File, assets *repository.Asset, clip *ai.ClipVisual, q *queue.Queue[any]) *processor {
	return &processor{
		ctx:             ctx,
		libraryLocation: libraryLocation,
		ffmpeg:          ff,
		files:           files,
		assets:          assets,
		clip:            clip,
		queue:           q,
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
			state.errored.Add(1)
			continue
		}
		switch task := t.(type) {
		case assetTask:
			if err := p.processAsset(task, et); err != nil {
				log.Printf("process file=%d path=%s: %v", task.FileID, task.Path, err)
				state.errored.Add(1)
				continue
			}
			state.processed.Add(1)
		case clipTask:
			if err := p.processClip(task); err != nil {
				// The asset keeps clip_embedded_at null, so the task is
				// re-enqueued on the next startup.
				log.Printf("clip asset=%d: %v", task.AssetID, err)
				continue
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
	thumbMs    int64
	geoMs      int64
	dbMs       int64
}

// processAsset checksums a file, stores (or reuses) its asset, and links the
// file row to the asset. An asset row existing implies its metadata and
// thumbnail were already extracted. On success a clip task is enqueued for
// the asset's thumbnail.
func (p *processor) processAsset(task assetTask, et *exiftoolbin.Exiftool) error {
	started := time.Now()

	absolutePath := media.ResolveLibraryPath(p.libraryLocation, task.Path)

	info, err := os.Stat(absolutePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", absolutePath, err)
	}

	var timings processTimings

	// Images readable as a stream are read from disk exactly once: the same
	// bytes feed both the checksum and the thumbnail generation, halving
	// disk I/O for the common case.
	var data []byte
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
	var checksum []byte
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

	dbStart := time.Now()
	asset, err := p.assets.GetByChecksum(p.ctx, checksum)
	timings.dbMs += time.Since(dbStart).Milliseconds()
	if err != nil {
		return fmt.Errorf("lookup asset by checksum: %w", err)
	}

	if asset == nil {
		if asset, timings, err = p.createAsset(task, et, absolutePath, checksum, info, data, timings); err != nil {
			return err
		}
	}

	dbStart = time.Now()
	if err := p.files.LinkAsset(p.ctx, task.FileID, asset.ID); err != nil {
		return fmt.Errorf("link file %d to asset %d: %w", task.FileID, asset.ID, err)
	}
	timings.dbMs += time.Since(dbStart).Milliseconds()

	if asset.ThumbnailStatus == entity.ThumbnailStatusOK && asset.ClipEmbeddedAt == 0 {
		p.queue.Push(clipTask{AssetID: asset.ID, Checksum: asset.Checksum}, clipPriority)
	}

	log.Printf(
		"processed file=%d path=%s asset=%d total_ms=%d read_ms=%d checksum_ms=%d metadata_ms=%d thumbnail_ms=%d geo_ms=%d db_ms=%d",
		task.FileID, task.Path, asset.ID,
		time.Since(started).Milliseconds(),
		timings.readMs, timings.checksumMs, timings.metadataMs, timings.thumbMs, timings.geoMs, timings.dbMs,
	)
	return nil
}

// processClip embeds the asset's thumbnail with the clip model and marks the
// asset embedded. Failures are recoverable: the asset keeps
// clip_embedded_at null and the task is re-enqueued on the next startup.
func (p *processor) processClip(task clipTask) error {
	started := time.Now()

	embedding, timings, err := p.clip.Embed(p.ctx, thumbnailPath(p.libraryLocation, task.Checksum))
	if err != nil {
		return fmt.Errorf("embed thumbnail: %w", err)
	}

	// The embedding is discarded for now; persisting it is a follow-up
	// (an embedding blob column on assets).
	if err := p.assets.MarkClipEmbedded(p.ctx, task.AssetID, time.Now().Unix()); err != nil {
		return err
	}

	log.Printf(
		"clipped asset=%d dim=%d total_ms=%d decode_ms=%d transform_ms=%d inference_ms=%d",
		task.AssetID, len(embedding),
		time.Since(started).Milliseconds(),
		timings.DecodeMs, timings.TransformMs, timings.InferenceMs,
	)
	return nil
}

// EnqueuePendingClipTasks re-adds clip tasks for assets that were indexed
// with a thumbnail but never embedded, e.g. because the process restarted
// while tasks were still queued. It returns the number of tasks enqueued.
func EnqueuePendingClipTasks(ctx context.Context, assets *repository.Asset, q *queue.Queue[any]) (int, error) {
	pending, err := assets.GetPendingClipEmbeddings(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range pending {
		q.Push(clipTask{AssetID: a.ID, Checksum: a.Checksum}, clipPriority)
	}
	return len(pending), nil
}

// createAsset probes the file's metadata, extracts the thumbnail and inserts
// its asset row. A concurrent worker may have stored the same content first;
// the asset is re-fetched by checksum so the row with the canonical id is
// returned.
func (p *processor) createAsset(task assetTask, et *exiftoolbin.Exiftool, absolutePath string, checksum []byte, info os.FileInfo, data []byte, timings processTimings) (*entity.Asset, processTimings, error) {
	metadataStart := time.Now()
	meta, err := et.ProbeMetadata(absolutePath)
	timings.metadataMs = time.Since(metadataStart).Milliseconds()
	if err != nil {
		return nil, timings, fmt.Errorf("probe metadata: %w", err)
	}

	mimeType := meta.MimeType
	if mimeType == "" {
		mimeType = exiftoolbin.MimeTypeFromPath(task.Path)
	}

	mtime := info.ModTime().Unix()
	asset := &entity.Asset{
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

	applySidecars(p.libraryLocation, task.Path, asset)

	geoStart := time.Now()
	err = applyCityCountry(asset, et)
	if err != nil {
		log.Printf("apply city country for %s: %v", task.Path, err)
	}
	timings.geoMs = time.Since(geoStart).Milliseconds()

	thumbStart := time.Now()
	if err := p.createThumbnail(checksum, absolutePath, data); err != nil {
		log.Printf("thumbnail for %s: %v", task.Path, err)
		asset.ThumbnailStatus = entity.ThumbnailStatusFailed
	}
	timings.thumbMs = time.Since(thumbStart).Milliseconds()

	insertStart := time.Now()
	if err := p.assets.Insert(p.ctx, asset); err != nil {
		return nil, timings, fmt.Errorf("store asset for %s: %w", task.Path, err)
	}

	stored, err := p.assets.GetByChecksum(p.ctx, checksum)
	if err != nil {
		timings.dbMs += time.Since(insertStart).Milliseconds()
		return nil, timings, fmt.Errorf("lookup asset by checksum: %w", err)
	}
	timings.dbMs += time.Since(insertStart).Milliseconds()

	return stored, timings, nil
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
