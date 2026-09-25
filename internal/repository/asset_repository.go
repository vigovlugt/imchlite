package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vigovlugt/imchlite/internal/entity"
)

// Asset owns the database pool for asset persistence.
type Asset struct {
	db *sql.DB
}

// NewAsset creates an AssetRepository backed by the given pool.
func NewAsset(db *sql.DB) *Asset {
	return &Asset{db: db}
}

// GetByChecksum returns the asset with the given content checksum, or nil if
// no asset holds that content yet.
func (r *Asset) GetByChecksum(ctx context.Context, checksum []byte) (*entity.Asset, error) {
	var (
		a                         entity.Asset
		mimeType                  sql.NullString
		assetType                 sql.NullInt64
		width, height, durationMs sql.NullInt64
	)
	err := r.db.QueryRowContext(ctx,
		`select id, mime_type, type, width, height, duration_ms, thumbnail_status
		 from assets where checksum = ?`, checksum).Scan(
		&a.ID, &mimeType, &assetType, &width, &height, &durationMs, &a.ThumbnailStatus)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get asset by checksum: %w", err)
	}

	a.Checksum = checksum
	a.MimeType = mimeType.String
	a.Type = entity.AssetType(assetType.Int64)
	a.Width = width.Int64
	a.Height = height.Int64
	a.DurationMs = durationMs.Int64
	return &a, nil
}

// Insert adds a new asset row. If an asset with the same checksum already
// exists (a concurrent worker won the race) the insert is a no-op.
func (r *Asset) Insert(ctx context.Context, a *entity.Asset) error {
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
		    width, height, duration_ms, orientation, thumbnail_status)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 on conflict (checksum) do nothing`,
		a.Checksum, mimeType, a.Type, a.FileCreatedAt, a.FileModifiedAt,
		dateTimeLocal, dateTime, timeZone, latitude, longitude, city, country,
		a.Width, a.Height, a.DurationMs, a.Orientation, a.ThumbnailStatus); err != nil {
		return fmt.Errorf("insert asset: %w", err)
	}
	return nil
}

// AssetCursor is the keyset pagination position: the capture time and id of
// the last asset of the previous page.
type AssetCursor struct {
	Time int64
	ID   int64
}

// AssetQuery holds the optional filters for listing assets. Nil/empty fields
// are not filtered on.
type AssetQuery struct {
	// IncludePaths: at least one of the asset's files matches one of these
	// SQLite GLOB patterns (e.g. "2024/*").
	IncludePaths []string
	// ExcludePaths: none of the asset's files matches any of these SQLite
	// GLOB patterns.
	ExcludePaths []string
	Type         *entity.AssetType
	City         *string
	Country      *string
	// From/Until bound the capture time (unix epoch seconds), inclusive.
	From  *int64
	Until *int64
	// Cursor is the keyset pagination position.
	Cursor *AssetCursor
	Limit  int
}

// hasOnlineFileCond restricts to assets that still have at least one copy
// reachable on disk; an asset whose every file went offline is not part of the
// library any more.
const hasOnlineFileCond = `exists (
	select 1 from files f where f.asset_id = a.id and f.is_offline = 0
)`

// Facets holds the filter suggestions derived from the library contents.
type Facets struct {
	Countries []string `json:"countries"`
	Cities    []string `json:"cities"`
	// Paths are the top-level directories present in the library.
	Paths      []string `json:"paths"`
	MinTime    *int64   `json:"minTime,omitempty"`
	MaxTime    *int64   `json:"maxTime,omitempty"`
	TotalCount int64    `json:"totalCount"`
}

// GetFacets returns the distinct countries, cities, top-level paths and the
// capture-time range across all non-deleted assets that are still online.
func (r *Asset) GetFacets(ctx context.Context) (Facets, error) {
	var f Facets

	if err := r.db.QueryRowContext(ctx,
		`select count(*),
		        min(coalesce(a.date_time_local, a.date_time)),
		        max(coalesce(a.date_time_local, a.date_time))
		 from assets a
		 where a.deleted_at is null and `+hasOnlineFileCond,
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
		`select distinct a.country from assets a
		 where a.deleted_at is null and a.country is not null and a.country != ''
		   and ` + hasOnlineFileCond + `
		 order by a.country`); err != nil {
		return f, fmt.Errorf("countries: %w", err)
	}
	if f.Cities, err = scanStrings(
		`select distinct a.city from assets a
		 where a.deleted_at is null and a.city is not null and a.city != ''
		   and ` + hasOnlineFileCond + `
		 order by a.city`); err != nil {
		return f, fmt.Errorf("cities: %w", err)
	}
	if f.Paths, err = scanStrings(
		`select distinct substr(path, 1, instr(path, '/') - 1)
		 from files
		 where instr(path, '/') > 0 and is_offline = 0
		 order by 1`); err != nil {
		return f, fmt.Errorf("paths: %w", err)
	}

	return f, nil
}

// LiveFileForChecksum returns a reachable file path holding the asset with
// the given checksum, preferring a match with the exact path prefix.
func (r *Asset) LiveFileForChecksum(ctx context.Context, checksum []byte) (string, string, error) {
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

// Query returns the assets matching the filters, newest capture time first,
// with stable keyset pagination on (capture time, id). The capture time is
// the wall-clock local date when known, otherwise the UTC instant. Assets
// without a single online file are left out.
func (r *Asset) Query(ctx context.Context, q AssetQuery) ([]entity.Asset, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	} else if limit > 1000 {
		limit = 1000
	}

	conds := []string{"a.deleted_at is null", hasOnlineFileCond}
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
	if len(q.IncludePaths) > 0 {
		parts := make([]string, len(q.IncludePaths))
		for i, p := range q.IncludePaths {
			parts[i] = "f.path glob ?"
			args = append(args, p)
		}
		conds = append(conds,
			"exists (select 1 from files f where f.asset_id = a.id and ("+strings.Join(parts, " or ")+"))")
	}
	for _, p := range q.ExcludePaths {
		conds = append(conds,
			"not exists (select 1 from files f where f.asset_id = a.id and f.path glob ?)")
		args = append(args, p)
	}
	if q.Cursor != nil {
		conds = append(conds, "(coalesce(a.date_time_local, a.date_time) < ? or (coalesce(a.date_time_local, a.date_time) = ? and a.id < ?))")
		args = append(args, q.Cursor.Time, q.Cursor.Time, q.Cursor.ID)
	}

	var sb strings.Builder
	sb.WriteString(`select a.id, a.checksum, a.mime_type, a.type,
		    a.date_time_local, a.date_time, a.time_zone, a.latitude, a.longitude,
		    a.city, a.country, a.width, a.height, a.duration_ms, a.orientation,
		    a.is_favorite, a.thumbnail_status,
		    (select group_concat(path, char(31))
		     from (select path from files
		           where asset_id = a.id and is_offline = 0
		           order by path))
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

	assets := []entity.Asset{}
	for rows.Next() {
		var (
			a                                      entity.Asset
			mimeType                               sql.NullString
			dateTimeLocal, dateTime                sql.NullInt64
			timeZone, city, country                sql.NullString
			latitude, longitude                    sql.NullFloat64
			width, height, durationMs, orientation sql.NullInt64
			paths                                  sql.NullString
		)
		if err := rows.Scan(
			&a.ID, &a.Checksum, &mimeType, &a.Type,
			&dateTimeLocal, &dateTime,
			&timeZone, &latitude, &longitude, &city, &country,
			&width, &height, &durationMs, &orientation, &a.IsFavorite,
			&a.ThumbnailStatus,
			&paths,
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
		if paths.String != "" {
			a.Paths = strings.Split(paths.String, "\x1f")
		}
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return assets, nil
}
