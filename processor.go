package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.design/x/chann"

	exiftoolbin "github.com/vigovlugt/imchlite/exiftool"
	"github.com/vigovlugt/imchlite/ffmpeg"
)

type assetTask struct {
	FileID int64
	Path   string
}

func newAssetQueue() *chann.Chann[assetTask] {
	return chann.New[assetTask]()
}

const (
	thumbnailSize    = 256
	thumbnailQuality = 80
)

// processor holds the shared dependencies of the asset workers.
type processor struct {
	ctx             context.Context
	libraryLocation string
	ffmpeg          *ffmpeg.FFmpeg
	files           *fileRepository
	assets          *assetRepository
}

// newProcessor creates a processor sharing the given repositories and
// extracted ffmpeg binary.
func newProcessor(ctx context.Context, libraryLocation string, ff *ffmpeg.FFmpeg, files *fileRepository, assets *assetRepository) *processor {
	return &processor{
		ctx:             ctx,
		libraryLocation: libraryLocation,
		ffmpeg:          ff,
		files:           files,
		assets:          assets,
	}
}

// worker consumes asset tasks from the queue until it is closed. Each worker
// runs its own exiftool process.
func (p *processor) worker(et *exiftoolbin.Exiftool, queue *chann.Chann[assetTask], state *indexerState) {
	for task := range queue.Out() {
		if err := p.process(task, et); err != nil {
			log.Printf("process file=%d path=%s: %v", task.FileID, task.Path, err)
			state.errored.Add(1)
			continue
		}
		state.processed.Add(1)
	}
}

// processTimings holds the wall-clock duration in milliseconds of each
// pipeline stage for a single processed asset.
type processTimings struct {
	checksumMs int64
	metadataMs int64
	thumbMs    int64
	geoMs      int64
}

// process checksums a file, stores (or reuses) its asset, and links the file
// row to the asset. An asset row existing implies its metadata and thumbnail
// were already extracted.
func (p *processor) process(task assetTask, et *exiftoolbin.Exiftool) error {
	started := time.Now()

	absolutePath := resolveLibraryPath(p.libraryLocation, task.Path)

	info, err := os.Stat(absolutePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", absolutePath, err)
	}

	var timings processTimings

	checksumStart := time.Now()
	checksum, err := fileChecksum(absolutePath)
	timings.checksumMs = time.Since(checksumStart).Milliseconds()
	if err != nil {
		return fmt.Errorf("checksum %s: %w", absolutePath, err)
	}

	asset, err := p.assets.getByChecksum(p.ctx, checksum)
	if err != nil {
		return fmt.Errorf("lookup asset by checksum: %w", err)
	}

	if asset == nil {
		if asset, timings, err = p.createAsset(task, et, absolutePath, checksum, info, timings); err != nil {
			return err
		}
	}

	if err := p.files.linkAsset(p.ctx, task.FileID, asset.ID); err != nil {
		return fmt.Errorf("link file %d to asset %d: %w", task.FileID, asset.ID, err)
	}

	log.Printf(
		"processed file=%d path=%s asset=%d total_ms=%d checksum_ms=%d metadata_ms=%d thumbnail_ms=%d geo_ms=%d",
		task.FileID, task.Path, asset.ID,
		time.Since(started).Milliseconds(),
		timings.checksumMs, timings.metadataMs, timings.thumbMs, timings.geoMs,
	)
	return nil
}

// createAsset probes the file's metadata, extracts the thumbnail and inserts
// its asset row. A concurrent worker may have stored the same content first;
// the asset is re-fetched by checksum so the row with the canonical id is
// returned.
func (p *processor) createAsset(task assetTask, et *exiftoolbin.Exiftool, absolutePath string, checksum []byte, info os.FileInfo, timings processTimings) (*Asset, processTimings, error) {
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
	asset := &Asset{
		Checksum:       checksum,
		MimeType:       mimeType,
		Type:           typeFromPath(task.Path),
		FileCreatedAt:  mtime,
		FileModifiedAt: mtime,
		LocalDateTime:  meta.LocalTakenAt,
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
	if err := p.createThumbnail(checksum, absolutePath); err != nil {
		log.Printf("thumbnail for %s: %v", task.Path, err)
	}
	timings.thumbMs = time.Since(thumbStart).Milliseconds()

	if err := p.assets.insert(p.ctx, asset); err != nil {
		return nil, timings, fmt.Errorf("store asset for %s: %w", task.Path, err)
	}

	stored, err := p.assets.getByChecksum(p.ctx, checksum)
	if err != nil {
		return nil, timings, fmt.Errorf("lookup asset by checksum: %w", err)
	}

	return stored, timings, nil
}

func (p *processor) createThumbnail(checksum []byte, absolutePath string) error {
	dir := filepath.Join(p.libraryLocation, ".imchlite", "thumbnails")
	hex := fmt.Sprintf("%x", checksum)
	dest := filepath.Join(dir, hex[0:2], hex[2:4], hex+".webp")

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create thumbnail dir: %w", err)
	}

	if err := p.ffmpeg.Thumbnail(p.ctx, absolutePath, dest, thumbnailSize, thumbnailQuality); err != nil {
		return err
	}

	return nil
}

// fileChecksum returns the sha256 of a file's bytes.
func fileChecksum(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func applyCityCountry(asset *Asset, exiftool *exiftoolbin.Exiftool) error {
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
