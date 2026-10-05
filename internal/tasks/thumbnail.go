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
)

const (
	thumbnailSize    = 256
	thumbnailQuality = 80
)

// processThumbnail generates the asset's webp thumbnail and stores the
// step's outcome in asset.ThumbnailStatus and the database. path, data and
// warm are as for processAsset. warmThumbnail reports that the thumbnail
// was just written, so it is in the page cache.
func (p *processor) processThumbnail(asset *entity.Asset, path string, data []byte, warm bool) (warmThumbnail bool, _ error) {
	if path == "" {
		var err error
		if path, _, err = p.assets.LiveFileForChecksum(p.ctx, asset.Checksum); err != nil {
			return false, fmt.Errorf("find file for asset: %w", err)
		}
	}

	started := time.Now()
	absolutePath := media.ResolveLibraryPath(p.libraryDir, path)
	waited, err := p.readDisk(warm, "thumbnail "+path, func() error {
		return p.createThumbnail(asset.Checksum, absolutePath, data)
	})
	if err != nil {
		if p.ctx.Err() != nil {
			return false, fmt.Errorf("thumbnail for %s: %w", path, err)
		}
		log.Printf("thumbnail for %s: %v", path, err)
		asset.ThumbnailStatus = entity.TaskStatusFailed
	} else {
		asset.ThumbnailStatus = entity.TaskStatusOK
		warmThumbnail = true
	}
	if err := p.assets.SetThumbnailStatus(p.ctx, asset.ID, asset.ThumbnailStatus); err != nil {
		return false, err
	}
	log.Printf("thumbnailed asset=%d path=%s total_ms=%d disk_wait_ms=%d", asset.ID, path, time.Since(started).Milliseconds(), waited.Milliseconds())
	return warmThumbnail, nil
}

func (p *processor) createThumbnail(checksum []byte, absolutePath string, data []byte) error {
	dest := thumbnailPath(p.dataDir, checksum)

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
func thumbnailPath(dataDir string, checksum []byte) string {
	dir := filepath.Join(dataDir, "thumbnails")
	hex := fmt.Sprintf("%x", checksum)
	return filepath.Join(dir, hex[0:2], hex[2:4], hex+".webp")
}
