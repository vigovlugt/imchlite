package tasks

import (
	"fmt"
	"log"
	"os"
	"time"

	exiftoolbin "github.com/vigovlugt/imchlite/internal/clients/exiftool"
	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
)

// createAsset probes the file's metadata and inserts its asset row with its
// metadata step ok and all other processing steps pending. A concurrent
// worker may have stored the same content first; the asset is re-fetched by
// checksum so the row with the canonical id is returned, and created reports
// whether this call inserted it.
func (p *processor) createAsset(task fileTask, et *exiftoolbin.Exiftool, absolutePath string, checksum []byte, info os.FileInfo, warm bool, timings processTimings) (asset *entity.Asset, created bool, _ processTimings, _ error) {
	metadataStart := time.Now()
	var meta exiftoolbin.MediaMetadata
	waited, err := p.readDisk(warm, "probe metadata "+task.Path, func() error {
		var err error
		meta, err = et.ProbeMetadata(absolutePath)
		return err
	})
	timings.metadataMs = (time.Since(metadataStart) - waited).Milliseconds()
	timings.diskWaitMs += waited.Milliseconds()
	if err != nil {
		return nil, false, timings, fmt.Errorf("probe metadata: %w", err)
	}

	mimeType := meta.MimeType
	if mimeType == "" {
		mimeType = exiftoolbin.MimeTypeFromPath(task.Path)
	}

	mtime := info.ModTime().Unix()
	asset = &entity.Asset{
		Checksum:       checksum,
		MimeType:       mimeType,
		Type:           media.TypeFromPath(task.Path),
		FileCreatedAt:  mtime,
		FileModifiedAt: mtime,
		LocalDateTime:  meta.LocalTakenAt,
		DateTime:       meta.TakenAtUTC,
		TimeZone:       meta.TimeZone,
		Latitude:       meta.Latitude,
		Longitude:      meta.Longitude,
		City:           meta.City,
		Country:        meta.Country,
		Width:          meta.Width,
		Height:         meta.Height,
		DurationMs:     meta.DurationMs,
		Orientation:    meta.Orientation,
		MetadataStatus: entity.TaskStatusOK,
	}

	// Sidecars are separate files, not warmed by reading the media file.
	waited, err = p.disk.Do(p.ctx, "sidecars "+task.Path, func() error {
		applySidecars(p.libraryDir, task.Path, asset)
		return nil
	})
	timings.diskWaitMs += waited.Milliseconds()
	if err != nil {
		return nil, false, timings, err
	}

	geoStart := time.Now()
	err = applyCityCountry(asset, et)
	if err != nil {
		log.Printf("apply city country for %s: %v", task.Path, err)
	}
	timings.geoMs = time.Since(geoStart).Milliseconds()

	insertStart := time.Now()
	created, err = p.assets.Insert(p.ctx, asset)
	if err != nil {
		return nil, false, timings, fmt.Errorf("store asset for %s: %w", task.Path, err)
	}

	stored, err := p.assets.GetByChecksum(p.ctx, checksum)
	timings.dbMs += time.Since(insertStart).Milliseconds()
	if err != nil {
		return nil, false, timings, fmt.Errorf("lookup asset by checksum: %w", err)
	}

	return stored, created, timings, nil
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
