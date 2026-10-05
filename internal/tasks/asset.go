package tasks

import (
	"context"

	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/queue"
	"github.com/vigovlugt/imchlite/internal/repository"
)

// assetTask runs the asset's pending processing steps (thumbnail, then
// clip). Each step's outcome is stored in its own status column, so a task
// re-enqueued at startup skips the steps that already finished.
type assetTask struct {
	Asset entity.Asset
}

// processAsset runs the asset's pending steps in order, skipping the ones
// whose status is already ok, or failed unless failed steps are retried. path and data are the file the
// asset was just created from and its bytes when already read; with an
// empty path an online file of the asset is looked up. warm reports that
// the file at path was just read and is in the page cache, so reading it
// again does not take the disk lock. A step interrupted by shutdown or a
// model that failed to load stays pending, to be retried on the next
// startup.
func (p *processor) processAsset(asset entity.Asset, path string, data []byte, warm bool) error {
	// A thumbnail created here was just written, so it is in the page cache.
	thumbnailWarm := false
	if p.shouldRun(asset.ThumbnailStatus) {
		var err error
		if thumbnailWarm, err = p.processThumbnail(&asset, path, data, warm); err != nil {
			return err
		}
	}

	if p.shouldRun(asset.ClipStatus) {
		if asset.ThumbnailStatus == entity.TaskStatusFailed {
			// Without a thumbnail there is nothing to embed.
			if asset.ClipStatus == entity.TaskStatusFailed {
				return nil
			}
			return p.assets.SetClipStatus(p.ctx, asset.ID, entity.TaskStatusFailed)
		}
		if err := p.processClip(asset, thumbnailWarm); err != nil {
			return err
		}
	}
	return nil
}

// EnqueuePendingAssetTasks re-adds asset tasks for assets with a pending
// step, e.g. because the process restarted mid-task, and with retryFailed
// also for assets with a failed step. It returns the number of tasks
// enqueued.
func EnqueuePendingAssetTasks(ctx context.Context, assets *repository.Asset, q *queue.Queue[any], retryFailed bool) (int, error) {
	pending, err := assets.GetAssetsWithPendingTasks(ctx, retryFailed)
	if err != nil {
		return 0, err
	}
	for _, a := range pending {
		q.Push(assetTask{Asset: a}, assetPriority)
	}
	return len(pending), nil
}
