package main

import (
	"context"
	"database/sql"
	"fmt"
)

// assetRepository owns the database pool for asset persistence.
type assetRepository struct {
	db *sql.DB
}

// NewAssetRepository creates an assetRepository backed by the given pool.
func NewAssetRepository(db *sql.DB) *assetRepository {
	return &assetRepository{db: db}
}

// getByChecksum returns the asset with the given content checksum, or nil if
// no asset holds that content yet.
func (r *assetRepository) getByChecksum(ctx context.Context, checksum []byte) (*Asset, error) {
	var (
		a                         Asset
		mimeType                  sql.NullString
		assetType                 sql.NullInt64
		width, height, durationMs sql.NullInt64
	)
	err := r.db.QueryRowContext(ctx,
		`select id, mime_type, type, width, height, duration_ms
		 from assets where checksum = ?`, checksum).Scan(
		&a.ID, &mimeType, &assetType, &width, &height, &durationMs)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get asset by checksum: %w", err)
	}

	a.Checksum = checksum
	a.MimeType = mimeType.String
	a.Type = AssetType(assetType.Int64)
	a.Width = width.Int64
	a.Height = height.Int64
	a.DurationMs = durationMs.Int64
	return &a, nil
}

// insert adds a new asset row. If an asset with the same checksum already
// exists (a concurrent worker won the race) the insert is a no-op.
func (r *assetRepository) insert(ctx context.Context, a *Asset) error {
	var mimeType, localDateTime, timeZone, city, country any
	if a.MimeType != "" {
		mimeType = a.MimeType
	}
	if a.LocalDateTime != 0 {
		localDateTime = a.LocalDateTime
	}
	if a.TimeZone != "" {
		timeZone = a.TimeZone
	}
	if a.City != "" {
		city = a.City
	}
	if a.Country != "" {
		country = a.Country
	}
	var latitude, longitude any
	if a.Latitude != 0 || a.Longitude != 0 {
		latitude, longitude = a.Latitude, a.Longitude
	}
	if _, err := r.db.ExecContext(ctx,
		`insert into assets (checksum, mime_type, type, file_created_at, file_modified_at,
		    local_date_time, time_zone, latitude, longitude, city, country,
		    width, height, duration_ms, orientation)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (checksum) do nothing`,
		a.Checksum, mimeType, a.Type, a.FileCreatedAt, a.FileModifiedAt,
		localDateTime, timeZone, latitude, longitude, city, country,
		a.Width, a.Height, a.DurationMs, a.Orientation); err != nil {
		return fmt.Errorf("insert asset: %w", err)
	}
	return nil
}
