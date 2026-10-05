package tasks

import (
	"fmt"
	"log"
	"time"

	exiftoolbin "github.com/vigovlugt/imchlite/internal/clients/exiftool"
	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
)

// metadataTask extracts an asset's metadata with exiftool and its
// sidecars. Its outcome is stored in the
// asset's metadata status column, so a task re-enqueued at startup is
// skipped once it finished. Path is the file the asset was just created
// from; when empty, an online file of the asset is looked up.
type metadataTask struct {
	Asset entity.Asset
	Path  string
}

// processMetadata probes the file's metadata, applies its sidecars and
// reverse geocodes its location, stores the result with the asset's
// metadata step ok, or failed when the file cannot be probed. A metadata
// step interrupted by shutdown stays pending, to be retried on the next
// startup.
func (p *processor) processMetadata(task metadataTask, et *exiftoolbin.Exiftool) error {
	asset, path := task.Asset, task.Path
	if path == "" {
		var err error
		if path, _, err = p.assets.LiveFileForChecksum(p.ctx, asset.Checksum); err != nil {
			return fmt.Errorf("find file for asset: %w", err)
		}
	}

	started := time.Now()
	absolutePath := media.ResolveLibraryPath(p.libraryDir, path)
	var meta exiftoolbin.MediaMetadata
	waited, err := p.disk.Do(p.ctx, "probe metadata "+path, func() error {
		var err error
		meta, err = et.ProbeMetadata(absolutePath)
		return err
	})
	if err != nil {
		if p.ctx.Err() != nil {
			return fmt.Errorf("probe metadata for %s: %w", path, err)
		}
		log.Printf("probe metadata for %s: %v", path, err)
		if err := p.assets.SetMetadataStatus(p.ctx, asset.ID, entity.TaskStatusFailed); err != nil {
			return err
		}
	} else {
		if meta.MimeType != "" {
			asset.MimeType = meta.MimeType
		}
		asset.LocalDateTime = meta.LocalTakenAt
		asset.DateTime = meta.TakenAtUTC
		asset.TimeZone = meta.TimeZone
		asset.Latitude = meta.Latitude
		asset.Longitude = meta.Longitude
		asset.City = meta.City
		asset.Country = meta.Country
		asset.Width = meta.Width
		asset.Height = meta.Height
		asset.DurationMs = meta.DurationMs
		asset.Orientation = meta.Orientation

		sidecarWaited, err := p.disk.Do(p.ctx, "sidecars "+path, func() error {
			applySidecars(p.libraryDir, path, &asset)
			return nil
		})
		waited += sidecarWaited
		if err != nil {
			return err
		}

		if err := applyCityCountry(&asset, et); err != nil {
			log.Printf("apply city country for %s: %v", path, err)
		}

		if err := p.assets.UpdateMetadata(p.ctx, &asset); err != nil {
			return err
		}
		log.Printf("extracted metadata asset=%d path=%s total_ms=%d disk_wait_ms=%d", asset.ID, path, time.Since(started).Milliseconds(), waited.Milliseconds())
	}
	return nil
}

func applyCityCountry(asset *entity.Asset, exiftool *exiftoolbin.Exiftool) error {
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
