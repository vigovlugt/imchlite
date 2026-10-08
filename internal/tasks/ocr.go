package tasks

import (
	"fmt"
	"image"
	"log"
	"time"

	"github.com/vigovlugt/imchlite/internal/ai"
	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
)

// ocrTask reads the text in an asset with the PP-OCR models. It decodes
// one of the asset's files itself, as the thumbnail is too small to read
// text from, so it does not depend on the thumbnail step. Its outcome is
// stored in the asset's ocr status column, so a task re-enqueued at
// startup is skipped once it finished.
type ocrTask struct {
	Asset entity.Asset
}

// processOCR reads the text boxes in one of the asset's online files and
// stores them with the asset's ocr step ok, or marks it failed when the
// file cannot be read. Videos are not read: their step is skipped.
// An ocr step interrupted by shutdown or a model that failed to load stays
// pending, to be retried on the next startup.
func (p *processor) processOCR(asset entity.Asset) error {
	if asset.Type == entity.AssetTypeVideo {
		return p.assets.SetOCRStatus(p.ctx, asset.ID, entity.TaskStatusSkipped)
	}

	path, _, err := p.assets.LiveFileForChecksum(p.ctx, asset.Checksum)
	if err != nil {
		return fmt.Errorf("find file for asset: %w", err)
	}

	if err := p.ocr.WaitLoad(p.ctx); err != nil {
		return fmt.Errorf("ocr models: %w", err)
	}

	started := time.Now()

	img, waited, err := p.decodeOCRPreview(path)
	if err != nil {
		if p.ctx.Err() != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
		log.Printf("ocr asset=%d path=%s: decode: %v", asset.ID, path, err)
		return p.assets.SetOCRStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
	}
	decodeMs := time.Since(started).Milliseconds() - waited.Milliseconds()

	boxes, timings, err := p.ocr.Read(p.ctx, img)
	if err != nil {
		if p.ctx.Err() != nil {
			return fmt.Errorf("read text in %s: %w", path, err)
		}
		log.Printf("ocr asset=%d path=%s: %v", asset.ID, path, err)
		return p.assets.SetOCRStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
	}

	if err := p.assets.ReplaceOCRBoxes(p.ctx, asset.ID, boxes); err != nil {
		return err
	}

	log.Printf(
		"ocred asset=%d path=%s boxes=%d total_ms=%d disk_wait_ms=%d decode_ms=%d detection_ms=%d recognition_ms=%d inference_wait_ms=%d inference_ms=%d",
		asset.ID, path, len(boxes),
		time.Since(started).Milliseconds(),
		waited.Milliseconds(), decodeMs,
		timings.DetectionMs, timings.RecognitionMs, timings.InferenceWaitMs, timings.InferenceMs,
	)
	return nil
}

// decodeOCRPreview has ffmpeg decode the library file at path, while the
// disk is held, into the preview the recognizer reads crops from, and
// returns how long it waited for the disk.
func (p *processor) decodeOCRPreview(path string) (*image.NRGBA, time.Duration, error) {
	var img *image.NRGBA
	waited, err := p.disk.Do(p.ctx, "ocr "+path, func() error {
		var err error
		img, err = p.ffmpeg.Decode(p.ctx, media.ResolveLibraryPath(p.libraryDir, path), ai.OCRPreviewSize)
		return err
	})
	return img, waited, err
}
