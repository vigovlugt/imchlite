package tasks

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/vigovlugt/imchlite/internal/entity"
)

// ocrTask reads the text in an asset with the PP-OCR models. It decodes
// one of the asset's files itself, as the thumbnail is too small to read
// text from, so it does not depend on the thumbnail step. Its outcome is
// stored in the asset's ocr status column, so a task re-enqueued at
// startup is skipped once it finished.
type ocrTask struct {
	Asset entity.Asset
}

// errOCRNotImplemented keeps ocr steps pending until the pipeline exists.
var errOCRNotImplemented = errors.New("not implemented")

// processOCR reads the text lines in one of the asset's online files and
// stores them with the asset's ocr step ok, or marks it failed when the
// file cannot be read. An ocr step interrupted by shutdown or a model that
// failed to load stays pending, to be retried on the next startup.
func (p *processor) processOCR(asset entity.Asset) error {
	path, _, err := p.assets.LiveFileForChecksum(p.ctx, asset.Checksum)
	if err != nil {
		return fmt.Errorf("find file for asset: %w", err)
	}

	started := time.Now()

	// TODO: decode path into a preview with ffmpeg under the disk lock into memory,
	// detect text boxes, recognize each box, and store the kept lines with
	// p.assets.ReplaceOCRLines. Videos: decide whether to read a frame or
	// skip them.
	lines, err := []entity.OCRLine(nil), errOCRNotImplemented
	if err != nil {
		if p.ctx.Err() != nil || errors.Is(err, errOCRNotImplemented) {
			return fmt.Errorf("read text in %s: %w", path, err)
		}
		log.Printf("ocr asset=%d path=%s: %v", asset.ID, path, err)
		return p.assets.SetOCRStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
	}

	if err := p.assets.ReplaceOCRLines(p.ctx, asset.ID, lines); err != nil {
		return err
	}

	log.Printf("ocred asset=%d path=%s lines=%d total_ms=%d",
		asset.ID, path, len(lines), time.Since(started).Milliseconds())
	return nil
}
