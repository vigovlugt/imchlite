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

// GetAssetsWithoutClipEmbedding returns assets whose thumbnail exists but
// that have no row in asset_clip_embeddings yet, i.e. pending clip tasks.
// They are re-enqueued at startup.
func (r *Asset) GetAssetsWithoutClipEmbedding(ctx context.Context) ([]entity.Asset, error) {
	rows, err := r.db.QueryContext(ctx,
		`select a.id, a.checksum from assets a
		 where a.thumbnail_status = 0 and a.deleted_at is null
		   and not exists (select 1 from asset_clip_embeddings e where e.asset_id = a.id)`)
	if err != nil {
		return nil, fmt.Errorf("assets without clip embedding: %w", err)
	}
	defer rows.Close()

	assets := []entity.Asset{}
	for rows.Next() {
		var a entity.Asset
		if err := rows.Scan(&a.ID, &a.Checksum); err != nil {
			return nil, fmt.Errorf("scan asset without clip embedding: %w", err)
		}
		assets = append(assets, a)
	}
	return assets, rows.Err()
}

// InsertClipEmbedding stores the clip embedding for an asset. If the asset
// already has an embedding (a concurrent worker won the race) the insert is
// a no-op.
func (r *Asset) InsertClipEmbedding(ctx context.Context, assetID int64, embedding []byte) error {
	if _, err := r.db.ExecContext(ctx,
		`insert into asset_clip_embeddings (asset_id, embedding) values (?, ?)
		 on conflict (asset_id) do nothing`, assetID, embedding); err != nil {
		return fmt.Errorf("insert clip embedding for asset %d: %w", assetID, err)
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
	// SimilarCursor is the keyset pagination position for similarity queries.
	SimilarCursor *SimilarCursor
	Limit         int
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

// assetSelectColumns is the shared select list for loading an asset together
// with the concatenated relative paths of its online files.
const assetSelectColumns = `a.id, a.checksum, a.mime_type, a.type,
		    a.date_time_local, a.date_time, a.time_zone, a.latitude, a.longitude,
		    a.city, a.country, a.width, a.height, a.duration_ms, a.orientation,
		    a.is_favorite, a.thumbnail_status,
		    (select group_concat(path, char(31))
		     from (select path from files
		           where asset_id = a.id and is_offline = 0
		           order by path))`

func clampLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

// assetFilterConds builds the where conditions and bound arguments for the
// optional filters in q. Cursors are left to the caller since the ordering
// they page over is query specific.
func assetFilterConds(q AssetQuery) ([]string, []any) {
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
	return conds, args
}

// whereClause joins the conditions with "and", parenthesizing each one.
func whereClause(conds []string) string {
	parts := make([]string, len(conds))
	for i, c := range conds {
		parts[i] = "(" + c + ")"
	}
	return strings.Join(parts, " and ")
}

// scanAsset reads one asset row produced by assetSelectColumns, plus any
// trailing columns appended through extra.
func scanAsset(rows *sql.Rows, extra ...any) (entity.Asset, error) {
	var (
		a                                      entity.Asset
		mimeType                               sql.NullString
		dateTimeLocal, dateTime                sql.NullInt64
		timeZone, city, country                sql.NullString
		latitude, longitude                    sql.NullFloat64
		width, height, durationMs, orientation sql.NullInt64
		paths                                  sql.NullString
	)
	dest := []any{
		&a.ID, &a.Checksum, &mimeType, &a.Type,
		&dateTimeLocal, &dateTime,
		&timeZone, &latitude, &longitude, &city, &country,
		&width, &height, &durationMs, &orientation, &a.IsFavorite,
		&a.ThumbnailStatus,
		&paths,
	}
	if err := rows.Scan(append(dest, extra...)...); err != nil {
		return a, fmt.Errorf("scan asset: %w", err)
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
	return a, nil
}

// scanAssets reads asset rows produced by assetSelectColumns.
func scanAssets(rows *sql.Rows) ([]entity.Asset, error) {
	assets := []entity.Asset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		assets = append(assets, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return assets, nil
}

// Query returns the assets matching the filters, newest capture time first,
// with stable keyset pagination on (capture time, id). The capture time is
// the wall-clock local date when known, otherwise the UTC instant. Assets
// without a single online file are left out.
func (r *Asset) Query(ctx context.Context, q AssetQuery) ([]entity.Asset, error) {
	limit := clampLimit(q.Limit)

	conds, args := assetFilterConds(q)
	if q.Cursor != nil {
		conds = append(conds, "(coalesce(a.date_time_local, a.date_time) < ? or (coalesce(a.date_time_local, a.date_time) = ? and a.id < ?))")
		args = append(args, q.Cursor.Time, q.Cursor.Time, q.Cursor.ID)
	}

	query := "select " + assetSelectColumns + `
		from assets a
		where ` + whereClause(conds) + `
		order by coalesce(a.date_time_local, a.date_time) desc, a.id desc
		limit ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query assets: %w", err)
	}
	defer rows.Close()

	return scanAssets(rows)
}

// SimilarCursor is the keyset pagination position for similarity results: the
// cosine distance and id of the last asset of the previous page.
type SimilarCursor struct {
	Distance float64
	ID       int64
}

// SimilarAsset pairs an asset with its cosine distance to the query embedding.
type SimilarAsset struct {
	Asset    entity.Asset
	Distance float64
}

// QuerySimilar returns the assets whose clip embedding is closest to the given
// query embedding, nearest first by cosine distance, with stable keyset
// pagination on (distance, id). The optional filters in q are applied before
// ranking. Assets without an online file or a clip embedding are left out.
func (r *Asset) QuerySimilar(ctx context.Context, embedding []byte, q AssetQuery) ([]SimilarAsset, error) {
	limit := clampLimit(q.Limit)

	conds, args := assetFilterConds(q)
	if q.SimilarCursor != nil {
		conds = append(conds, "(v.distance > ? or (v.distance = ? and v.rowid > ?))")
		args = append(args, q.SimilarCursor.Distance, q.SimilarCursor.Distance, q.SimilarCursor.ID)
	}

	// streaming:1 lets vec1 keep yielding neighbors past k until SQLite has
	// satisfied the limit, which matters because the join and filters can
	// discard candidates.
	query := "select " + assetSelectColumns + `, v.distance
		from asset_clip_embeddings_vec(?, ?) v
		join assets a on a.id = v.rowid
		where ` + whereClause(conds) + `
		order by v.distance asc, v.rowid asc
		limit ?`
	args = append([]any{embedding, fmt.Sprintf(`{k:%d, streaming:1}`, limit)}, args...)
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query similar assets: %w", err)
	}
	defer rows.Close()

	found := []SimilarAsset{}
	for rows.Next() {
		var distance float64
		a, err := scanAsset(rows, &distance)
		if err != nil {
			return nil, err
		}
		found = append(found, SimilarAsset{Asset: a, Distance: distance})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate similar assets: %w", err)
	}
	return found, nil
}
