package tasks

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/queue"
)

const (
	thumbnailSize    = 256
	thumbnailQuality = 80
	// maxBufferedSourceSize is the largest source file read into memory to
	// be decoded without holding the disk.
	maxBufferedSourceSize = 64 << 20
)

// bufferedExtensions are the image formats ffmpeg decodes from a pipe. Their
// source file is read under the disk lock and decoded after releasing it.
// Other formats need a seekable input (HEIC and AVIF containers, TIFF-based
// raw formats) or are videos, of which ffmpeg reads only a small part; for
// those ffmpeg reads the file itself while the disk is held.
var bufferedExtensions = map[string]struct{}{
	".jpg": {}, ".jpeg": {}, ".jpe": {}, ".png": {}, ".webp": {}, ".gif": {}, ".bmp": {},
}

// thumbnailTask generates an asset's thumbnail, then enqueues its clip
// task. Its outcome is stored in the asset's thumbnail status column, so a
// task re-enqueued at startup is skipped once it finished.
type thumbnailTask struct {
	Asset entity.Asset
}

// processThumbnail generates the asset's webp thumbnail from one of its
// online files, stores the step's outcome in the database and, if the
// asset's clip step needs to run, enqueues its clip task on q. A thumbnail
// interrupted by shutdown stays pending, to be retried on the next startup.
func (p *processor) processThumbnail(asset entity.Asset, q *queue.Queue[any]) error {
	path, _, err := p.assets.LiveFileForChecksum(p.ctx, asset.Checksum)
	if err != nil {
		return fmt.Errorf("find file for asset: %w", err)
	}

	started := time.Now()
	waited, err := p.createThumbnail(asset.Checksum, path)
	if err != nil {
		if p.ctx.Err() != nil {
			return fmt.Errorf("thumbnail for %s: %w", path, err)
		}
		log.Printf("thumbnail for %s: %v", path, err)
		asset.ThumbnailStatus = entity.TaskStatusFailed
	} else {
		asset.ThumbnailStatus = entity.TaskStatusOK
	}
	if err := p.assets.SetThumbnailStatus(p.ctx, asset.ID, asset.ThumbnailStatus); err != nil {
		return err
	}
	log.Printf("thumbnailed asset=%d path=%s total_ms=%d disk_wait_ms=%d", asset.ID, path, time.Since(started).Milliseconds(), waited.Milliseconds())

	if p.shouldRun(asset.ClipStatus) {
		q.Push(clipTask{Asset: asset}, clipPriority)
	}
	return nil
}

// createThumbnail writes the thumbnail of the asset with the given checksum
// from the library file at path, and returns how long it waited for the
// disk. Small files in a buffered format are read into memory under the
// disk lock and decoded after releasing it; any other file is decoded by
// ffmpeg while the disk is held.
func (p *processor) createThumbnail(checksum []byte, path string) (time.Duration, error) {
	absolutePath := media.ResolveLibraryPath(p.libraryDir, path)
	dest := thumbnailPath(p.dataDir, checksum)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, fmt.Errorf("create thumbnail dir: %w", err)
	}

	var source []byte
	buffered := false
	waited, err := p.disk.Do(p.ctx, "thumbnail "+path, func() error {
		ext, _ := media.LowerExtension(path)
		if _, ok := bufferedExtensions[ext]; ok {
			info, err := os.Stat(absolutePath)
			if err != nil {
				return err
			}
			if info.Size() <= maxBufferedSourceSize {
				source, err = os.ReadFile(absolutePath)
				buffered = err == nil
				return err
			}
		}
		return p.ffmpeg.Thumbnail(p.ctx, absolutePath, dest, thumbnailSize, thumbnailQuality)
	})
	if err != nil || !buffered {
		return waited, err
	}
	return waited, p.ffmpeg.ThumbnailFromReader(p.ctx, bytes.NewReader(source), dest, thumbnailSize, thumbnailQuality)
}

// thumbnailPath returns the canonical thumbnail location for the asset with
// the given checksum.
func thumbnailPath(dataDir string, checksum []byte) string {
	dir := filepath.Join(dataDir, "thumbnails")
	hex := fmt.Sprintf("%x", checksum)
	return filepath.Join(dir, hex[0:2], hex[2:4], hex+".webp")
}
