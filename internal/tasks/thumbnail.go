package tasks

import (
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
)

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
	absolutePath := media.ResolveLibraryPath(p.libraryDir, path)
	waited, err := p.disk.Do(p.ctx, "thumbnail "+path, func() error {
		return p.createThumbnail(asset.Checksum, absolutePath)
	})
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
		// A thumbnail created here was just written, so it is in the page
		// cache.
		q.Push(clipTask{Asset: asset, ThumbnailWarm: asset.ThumbnailStatus == entity.TaskStatusOK}, clipPriority)
	}
	return nil
}

func (p *processor) createThumbnail(checksum []byte, absolutePath string) error {
	dest := thumbnailPath(p.dataDir, checksum)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create thumbnail dir: %w", err)
	}

	return p.ffmpeg.Thumbnail(p.ctx, absolutePath, dest, thumbnailSize, thumbnailQuality)
}

// thumbnailPath returns the canonical thumbnail location for the asset with
// the given checksum.
func thumbnailPath(dataDir string, checksum []byte) string {
	dir := filepath.Join(dataDir, "thumbnails")
	hex := fmt.Sprintf("%x", checksum)
	return filepath.Join(dir, hex[0:2], hex[2:4], hex+".webp")
}
