package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	var mimeType, dateTimeLocal, dateTime, timeZone, city, country any
	if a.MimeType != "" {
		mimeType = a.MimeType
	}
	if a.LocalDateTime != 0 {
		dateTimeLocal = a.LocalDateTime
	}
	if a.DateTime != 0 {
		dateTime = a.DateTime
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
		    date_time_local, date_time, time_zone, latitude, longitude, city, country,
		    width, height, duration_ms, orientation)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (checksum) do nothing`,
		a.Checksum, mimeType, a.Type, a.FileCreatedAt, a.FileModifiedAt,
		dateTimeLocal, dateTime, timeZone, latitude, longitude, city, country,
		a.Width, a.Height, a.DurationMs, a.Orientation); err != nil {
		return fmt.Errorf("insert asset: %w", err)
	}
	return nil
}

// assetCursor is the keyset pagination position: the capture time and id of
// the last asset of the previous page.
type assetCursor struct {
	Time int64
	ID   int64
}

// assetQuery holds the optional filters for listing assets. Nil/empty fields
// are not filtered on.
type assetQuery struct {
	// IncludePaths: at least one of the asset's files is under one of these
	// path prefixes.
	IncludePaths []string
	// ExcludePaths: none of the asset's files is under any of these path
	// prefixes.
	ExcludePaths []string
	Type         *AssetType
	City         *string
	Country      *string
	// From/Until bound the capture time (unix epoch seconds), inclusive.
	From  *int64
	Until *int64
	// Cursor is the keyset pagination position.
	Cursor *assetCursor
	Limit  int
}

// likePattern escapes the sql like wildcards in a path prefix and appends %.
func likePattern(prefix string) string {
	pattern := make([]byte, 0, len(prefix)+1)
	for i := 0; i < len(prefix); i++ {
		switch c := prefix[i]; c {
		case '\\', '%', '_':
			pattern = append(pattern, '\\', c)
		default:
			pattern = append(pattern, c)
		}
	}
	return string(pattern) + "%"
}

// facets holds the filter suggestions derived from the library contents.
type facets struct {
	Countries []string `json:"countries"`
	Cities    []string `json:"cities"`
	// Paths are the top-level directories present in the library.
	Paths      []string `json:"paths"`
	MinTime    *int64   `json:"minTime,omitempty"`
	MaxTime    *int64   `json:"maxTime,omitempty"`
	TotalCount int64    `json:"totalCount"`
}

// getFacets returns the distinct countries, cities, top-level paths and the
// capture-time range across all non-deleted assets.
func (r *assetRepository) getFacets(ctx context.Context) (facets, error) {
	var f facets

	if err := r.db.QueryRowContext(ctx,
		`select count(*),
		        min(coalesce(date_time_local, date_time)),
		        max(coalesce(date_time_local, date_time))
		 from assets
		 where deleted_at is null`,
	).Scan(&f.TotalCount, &f.MinTime, &f.MaxTime); err != nil {
		return f, fmt.Errorf("asset stats: %w", err)
	}

	scanStrings := func(query string) ([]string, error) {
		rows, err := r.db.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		values := []string{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		return values, rows.Err()
	}

	var err error
	if f.Countries, err = scanStrings(
		`select distinct country from assets
		 where deleted_at is null and country is not null and country != ''
		 order by country`); err != nil {
		return f, fmt.Errorf("countries: %w", err)
	}
	if f.Cities, err = scanStrings(
		`select distinct city from assets
		 where deleted_at is null and city is not null and city != ''
		 order by city`); err != nil {
		return f, fmt.Errorf("cities: %w", err)
	}
	if f.Paths, err = scanStrings(
		`select distinct substr(path, 1, instr(path, '/') - 1)
		 from files
		 where instr(path, '/') > 0
		 order by 1`); err != nil {
		return f, fmt.Errorf("paths: %w", err)
	}

	return f, nil
}

// liveFileForChecksum returns a reachable file path holding the asset with
// the given checksum, preferring a match with the exact path prefix.
func (r *assetRepository) liveFileForChecksum(ctx context.Context, checksum []byte) (string, string, error) {
	var path, mimeType string
	err := r.db.QueryRowContext(ctx,
		`select f.path, coalesce(a.mime_type, '')
		 from assets a
		 join files f on f.asset_id = a.id and f.is_offline = 0
		 where a.checksum = ?
		 order by f.path
		 limit 1`, checksum).Scan(&path, &mimeType)
	if err == sql.ErrNoRows {
		return "", "", sql.ErrNoRows
	}
	if err != nil {
		return "", "", fmt.Errorf("file for checksum: %w", err)
	}
	return path, mimeType, nil
}

// query returns the assets matching the filters, newest capture time first,
// with stable keyset pagination on (capture time, id). The capture time is
// the wall-clock local date when known, otherwise the UTC instant.
func (r *assetRepository) query(ctx context.Context, q assetQuery) ([]Asset, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}

	conds := []string{"a.deleted_at is null"}
	args := make([]any, 0, len(q.IncludePaths)+len(q.ExcludePaths)+8)

	if q.Type != nil {
		conds = append(conds, "a.type = ?")
		args = append(args, *q.Type)
	}
	if q.City != nil {
		conds = append(conds, "a.city = ?")
		args = append(args, *q.City)
	}
	if q.Country != nil {
		conds = append(conds, "a.country = ?")
		args = append(args, *q.Country)
	}
	if q.From != nil {
		conds = append(conds, "coalesce(a.date_time_local, a.date_time) >= ?")
		args = append(args, *q.From)
	}
	if q.Until != nil {
		conds = append(conds, "coalesce(a.date_time_local, a.date_time) <= ?")
		args = append(args, *q.Until)
	}
	for _, p := range q.IncludePaths {
		conds = append(conds,
			"exists (select 1 from files f where f.asset_id = a.id and f.path like ? escape '\\')")
		args = append(args, likePattern(p))
	}
	for _, p := range q.ExcludePaths {
		conds = append(conds,
			"not exists (select 1 from files f where f.asset_id = a.id and f.path like ? escape '\\')")
		args = append(args, likePattern(p))
	}
	if q.Cursor != nil {
		conds = append(conds, "(coalesce(a.date_time_local, a.date_time) < ? or (coalesce(a.date_time_local, a.date_time) = ? and a.id < ?))")
		args = append(args, q.Cursor.Time, q.Cursor.Time, q.Cursor.ID)
	}

	var sb strings.Builder
	sb.WriteString(`select a.id, a.checksum, a.mime_type, a.type,
		    a.date_time_local, a.date_time, a.time_zone, a.latitude, a.longitude,
		    a.city, a.country, a.width, a.height, a.duration_ms, a.orientation,
		    a.is_favorite
		from assets a
		where `)
	for i, cond := range conds {
		if i > 0 {
			sb.WriteString(" and ")
		}
		sb.WriteString("(")
		sb.WriteString(cond)
		sb.WriteString(")")
	}
	sb.WriteString(`
		order by coalesce(a.date_time_local, a.date_time) desc, a.id desc
		limit ?`)
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query assets: %w", err)
	}
	defer rows.Close()

	assets := []Asset{}
	for rows.Next() {
		var (
			a                                      Asset
			mimeType                               sql.NullString
			dateTimeLocal, dateTime                sql.NullInt64
			timeZone, city, country                sql.NullString
			latitude, longitude                    sql.NullFloat64
			width, height, durationMs, orientation sql.NullInt64
		)
		if err := rows.Scan(
			&a.ID, &a.Checksum, &mimeType, &a.Type,
			&dateTimeLocal, &dateTime,
			&timeZone, &latitude, &longitude, &city, &country,
			&width, &height, &durationMs, &orientation, &a.IsFavorite,
		); err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		a.MimeType = mimeType.String
		a.LocalDateTime = dateTimeLocal.Int64
		a.DateTime = dateTime.Int64
		a.TimeZone = timeZone.String
		a.Latitude = latitude.Float64
		a.Longitude = longitude.Float64
		a.City = city.String
		a.Country = country.String
		a.Width = width.Int64
		a.Height = height.Int64
		a.DurationMs = durationMs.Int64
		a.Orientation = orientation.Int64
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return assets, nil
}
