package tasks

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/utils"
)

// clipTask embeds an asset's thumbnail with the clip model. Its outcome is
// stored in the asset's clip status column, so a task re-enqueued at
// startup is skipped once it finished.
type clipTask struct {
	Asset entity.Asset
}

// processClip embeds the asset's thumbnail with the clip model and marks the
// asset's clip step ok, or failed when there is no thumbnail or it cannot
// be embedded. A clip step interrupted by shutdown or a model that failed
// to load stays pending, to be retried on the next startup.
func (p *processor) processClip(asset entity.Asset) error {
	if asset.ThumbnailStatus == entity.TaskStatusFailed {
		// Without a thumbnail there is nothing to embed.
		return p.assets.SetClipStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
	}

	if err := p.clip.WaitLoad(p.ctx); err != nil {
		return fmt.Errorf("clip model: %w", err)
	}

	started := time.Now()

	var thumbnail []byte
	waited, err := p.disk.Do(p.ctx, fmt.Sprintf("read thumbnail asset=%d", asset.ID), func() error {
		var err error
		thumbnail, err = os.ReadFile(thumbnailPath(p.dataDir, asset.Checksum))
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
		"clipped asset=%d dim=%d total_ms=%d disk_wait_ms=%d decode_ms=%d transform_ms=%d inference_wait_ms=%d inference_ms=%d",
		asset.ID, len(embedding),
		time.Since(started).Milliseconds(),
		waited.Milliseconds(),
		timings.DecodeMs, timings.TransformMs, timings.InferenceWaitMs, timings.InferenceMs,
	)
	return nil
}
