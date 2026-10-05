package tasks

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	exiftoolbin "github.com/vigovlugt/imchlite/internal/clients/exiftool"
	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/queue"
)

// checksumTask checksums a file and links it to its (possibly new) asset.
type checksumTask struct {
	FileID int64
	Path   string
}

const (
	copyBufferSize = 1 << 20
	// largeFileSize is the size above which a file's checksum is logged on
	// its own.
	largeFileSize = 256 << 20
)

// processChecksum checksums a file, stores (or reuses) its asset, and links
// the file row to the asset. A new asset is inserted with only its
// checksum, type and file times, and its metadata and thumbnail tasks are
// enqueued on q; neither depends on the other.
// The file is read under the disk lock.
func (p *processor) processChecksum(task checksumTask, q *queue.Queue[any]) error {
	started := time.Now()

	absolutePath := media.ResolveLibraryPath(p.libraryDir, task.Path)

	var (
		info       os.FileInfo
		checksum   []byte
		checksumMs int64
	)
	waited, err := p.disk.Do(p.ctx, "checksum "+task.Path, func() error {
		var err error
		if info, err = os.Stat(absolutePath); err != nil {
			return fmt.Errorf("stat %s: %w", absolutePath, err)
		}

		checksumStart := time.Now()
		checksum, err = fileChecksum(absolutePath)
		elapsed := time.Since(checksumStart)
		checksumMs = elapsed.Milliseconds()
		if err != nil {
			return fmt.Errorf("checksum %s: %w", absolutePath, err)
		}
		if info.Size() > largeFileSize {
			// Large files are checksummed for minutes before the file's
			// checksummed line; report them on their own.
			log.Printf("checksummed large file=%d path=%s size_mb=%d checksum_ms=%d mb_per_s=%.0f",
				task.FileID, task.Path, info.Size()>>20, checksumMs, float64(info.Size()>>20)/elapsed.Seconds())
		}
		return nil
	})
	if err != nil {
		return err
	}

	dbStart := time.Now()
	asset, err := p.assets.GetByChecksum(p.ctx, checksum)
	if err != nil {
		return fmt.Errorf("lookup asset by checksum: %w", err)
	}

	created := false
	if asset == nil {
		if asset, created, err = p.insertAsset(task.Path, checksum, info); err != nil {
			return err
		}
	}

	if err := p.files.LinkAsset(p.ctx, task.FileID, asset.ID); err != nil {
		return fmt.Errorf("link file %d to asset %d: %w", task.FileID, asset.ID, err)
	}
	dbMs := time.Since(dbStart).Milliseconds()

	log.Printf(
		"checksummed file=%d path=%s asset=%d total_ms=%d disk_wait_ms=%d checksum_ms=%d db_ms=%d",
		task.FileID, task.Path, asset.ID,
		time.Since(started).Milliseconds(), waited.Milliseconds(), checksumMs, dbMs,
	)

	if created {
		q.Push(metadataTask{Asset: *asset, Path: task.Path}, metadataPriority)
		q.Push(thumbnailTask{Asset: *asset}, thumbnailPriority)
	}
	return nil
}

// insertAsset inserts the asset for a file's content with all processing
// steps pending. A concurrent worker may have stored the same content
// first; the asset is re-fetched by checksum so the row with the canonical
// id is returned, and created reports whether this call inserted it.
func (p *processor) insertAsset(path string, checksum []byte, info os.FileInfo) (asset *entity.Asset, created bool, _ error) {
	mtime := info.ModTime().Unix()
	created, err := p.assets.Insert(p.ctx, &entity.Asset{
		Checksum:       checksum,
		MimeType:       exiftoolbin.MimeTypeFromPath(path),
		Type:           media.TypeFromPath(path),
		FileCreatedAt:  mtime,
		FileModifiedAt: mtime,
	})
	if err != nil {
		return nil, false, fmt.Errorf("store asset for %s: %w", path, err)
	}

	asset, err = p.assets.GetByChecksum(p.ctx, checksum)
	if err != nil {
		return nil, false, fmt.Errorf("lookup asset by checksum: %w", err)
	}
	return asset, created, nil
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
