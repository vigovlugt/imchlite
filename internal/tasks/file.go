package tasks

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	exiftoolbin "github.com/vigovlugt/imchlite/internal/clients/exiftool"
	"github.com/vigovlugt/imchlite/internal/media"
)

// fileTask checksums a file and links it to its (possibly new) asset.
type fileTask struct {
	FileID int64
	Path   string
}

const (
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

	absolutePath := media.ResolveLibraryPath(p.libraryDir, task.Path)

	var (
		info     os.FileInfo
		data     []byte
		checksum []byte
		timings  processTimings
	)
	waited, err := p.disk.Do(p.ctx, "checksum "+task.Path, func() error {
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
		if err == nil && info.Size() > maxWarmFileSize {
			// Large files are checksummed for minutes before the file's
			// processed line; report them on their own.
			elapsed := time.Since(checksumStart)
			log.Printf("checksummed large file=%d path=%s size_mb=%d checksum_ms=%d mb_per_s=%.0f",
				task.FileID, task.Path, info.Size()>>20, elapsed.Milliseconds(), float64(info.Size()>>20)/elapsed.Seconds())
		}
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
