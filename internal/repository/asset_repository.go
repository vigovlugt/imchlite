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
		`select id, mime_type, type, width, height, duration_ms, metadata_status, thumbnail_status, clip_status, ocr_status
		 from assets where checksum = ?`, checksum).Scan(
		&a.ID, &mimeType, &assetType, &width, &height, &durationMs, &a.MetadataStatus, &a.ThumbnailStatus, &a.ClipStatus, &a.OCRStatus)
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

// ClipEmbeddingByChecksum returns the id and stored clip embedding of the
// asset with the given checksum. The embedding is nil if the asset exists but
// has not been embedded yet; ok is false if no asset has that checksum.
func (r *Asset) ClipEmbeddingByChecksum(ctx context.Context, checksum []byte) (id int64, embedding []byte, ok bool, err error) {
	err = r.db.QueryRowContext(ctx,
		`select a.id, e.embedding
		 from assets a
		 left join asset_clip_embeddings e on e.asset_id = a.id
		 where a.checksum = ? and a.deleted_at is null`, checksum).Scan(&id, &embedding)
	if err == sql.ErrNoRows {
		return 0, nil, false, nil
	}
	if err != nil {
		return 0, nil, false, fmt.Errorf("get clip embedding by checksum: %w", err)
	}
	return id, embedding, true, nil
}

// Insert adds a new asset row holding only what is known before its
// metadata is extracted (checksum, mime type, type and file times), with
// all processing steps pending. If an asset with the same checksum already
// exists (a concurrent worker won the race) the insert is a no-op. It
// reports whether a row was inserted.
func (r *Asset) Insert(ctx context.Context, a *entity.Asset) (bool, error) {
	var mimeType any
	if a.MimeType != "" {
		mimeType = a.MimeType
	}
	res, err := r.db.ExecContext(ctx,
		`insert into assets (checksum, mime_type, type, file_created_at, file_modified_at)
		 values (?, ?, ?, ?, ?)
		 on conflict (checksum) do nothing`,
		a.Checksum, mimeType, a.Type, a.FileCreatedAt, a.FileModifiedAt)
	if err != nil {
		return false, fmt.Errorf("insert asset: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert asset: rows affected: %w", err)
	}
	return n > 0, nil
}

// UpdateMetadata stores the asset's extracted metadata and marks its
// metadata step ok.
func (r *Asset) UpdateMetadata(ctx context.Context, a *entity.Asset) error {
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
		`update assets set mime_type = coalesce(?, mime_type), date_time_local = ?, date_time = ?,
		    time_zone = ?, latitude = ?, longitude = ?, city = ?, country = ?,
		    width = ?, height = ?, duration_ms = ?, orientation = ?,
		    metadata_status = ?, updated_at = unixepoch()
		 where id = ?`,
		mimeType, dateTimeLocal, dateTime, timeZone, latitude, longitude, city, country,
		a.Width, a.Height, a.DurationMs, a.Orientation, entity.TaskStatusOK, a.ID); err != nil {
		return fmt.Errorf("update metadata for asset %d: %w", a.ID, err)
	}
	return nil
}

// SetMetadataStatus records the outcome of an asset's metadata step.
func (r *Asset) SetMetadataStatus(ctx context.Context, assetID int64, status entity.TaskStatus) error {
	if _, err := r.db.ExecContext(ctx,
		`update assets set metadata_status = ?, updated_at = unixepoch() where id = ?`,
		status, assetID); err != nil {
		return fmt.Errorf("set metadata status for asset %d: %w", assetID, err)
	}
	return nil
}

// GetAssetsWithPendingTasks returns the assets that still have a pending
// processing step and an online file to process it from, e.g. because the
// process restarted mid-task. With includeFailed, assets with a failed step
// are returned too. They are re-enqueued after the library walk.
func (r *Asset) GetAssetsWithPendingTasks(ctx context.Context, includeFailed bool) ([]entity.Asset, error) {
	statuses := "0"
	if includeFailed {
		statuses = "0, 2"
	}
	rows, err := r.db.QueryContext(ctx,
		`select a.id, a.checksum, a.type, a.metadata_status, a.thumbnail_status, a.clip_status, a.ocr_status from assets a
		 where (a.metadata_status in (`+statuses+`) or a.thumbnail_status in (`+statuses+`)
		        or a.clip_status in (`+statuses+`) or a.ocr_status in (`+statuses+`))
		   and a.deleted_at is null and `+hasOnlineFileCond)
	if err != nil {
		return nil, fmt.Errorf("assets with pending tasks: %w", err)
	}
	defer rows.Close()

	assets := []entity.Asset{}
	for rows.Next() {
		var a entity.Asset
		if err := rows.Scan(&a.ID, &a.Checksum, &a.Type, &a.MetadataStatus, &a.ThumbnailStatus, &a.ClipStatus, &a.OCRStatus); err != nil {
			return nil, fmt.Errorf("scan asset with pending tasks: %w", err)
		}
		assets = append(assets, a)
	}
	return assets, rows.Err()
}

// SetThumbnailStatus records the outcome of an asset's thumbnail step.
func (r *Asset) SetThumbnailStatus(ctx context.Context, assetID int64, status entity.TaskStatus) error {
	if _, err := r.db.ExecContext(ctx,
		`update assets set thumbnail_status = ?, updated_at = unixepoch() where id = ?`,
		status, assetID); err != nil {
		return fmt.Errorf("set thumbnail status for asset %d: %w", assetID, err)
	}
	return nil
}

// SetClipStatus records the outcome of an asset's clip step.
func (r *Asset) SetClipStatus(ctx context.Context, assetID int64, status entity.TaskStatus) error {
	if _, err := r.db.ExecContext(ctx,
		`update assets set clip_status = ?, updated_at = unixepoch() where id = ?`,
		status, assetID); err != nil {
		return fmt.Errorf("set clip status for asset %d: %w", assetID, err)
	}
	return nil
}

// InsertClipEmbedding stores the clip embedding for an asset and marks its
// clip step ok. If the asset already has an embedding (a concurrent worker
// won the race) the insert is a no-op.
func (r *Asset) InsertClipEmbedding(ctx context.Context, assetID int64, embedding []byte) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert clip embedding for asset %d: begin: %w", assetID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`insert into asset_clip_embeddings (asset_id, embedding) values (?, ?)
		 on conflict do nothing`, assetID, embedding); err != nil {
		return fmt.Errorf("insert clip embedding for asset %d: %w", assetID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`update assets set clip_status = ?, updated_at = unixepoch() where id = ?`,
		entity.TaskStatusOK, assetID); err != nil {
		return fmt.Errorf("set clip status for asset %d: %w", assetID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert clip embedding for asset %d: commit: %w", assetID, err)
	}
	return nil
}

// SetOCRStatus records the outcome of an asset's ocr step.
func (r *Asset) SetOCRStatus(ctx context.Context, assetID int64, status entity.TaskStatus) error {
	if _, err := r.db.ExecContext(ctx,
		`update assets set ocr_status = ?, updated_at = unixepoch() where id = ?`,
		status, assetID); err != nil {
		return fmt.Errorf("set ocr status for asset %d: %w", assetID, err)
	}
	return nil
}

// ReplaceOCRBoxes stores the text boxes read from an asset, in reading
// order, replacing any it had, and marks its ocr step ok.
func (r *Asset) ReplaceOCRBoxes(ctx context.Context, assetID int64, boxes []entity.OCRBox) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace ocr boxes for asset %d: begin: %w", assetID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `delete from asset_ocr_boxes where asset_id = ?`, assetID); err != nil {
		return fmt.Errorf("replace ocr boxes for asset %d: delete: %w", assetID, err)
	}
	for i, b := range boxes {
		if _, err := tx.ExecContext(ctx,
			`insert into asset_ocr_boxes (asset_id, position, line, x1, y1, x2, y2, x3, y3, x4, y4, box_score, text_score, text)
			 values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			assetID, i, b.Line,
			b.Corners[0].X, b.Corners[0].Y, b.Corners[1].X, b.Corners[1].Y,
			b.Corners[2].X, b.Corners[2].Y, b.Corners[3].X, b.Corners[3].Y,
			b.BoxScore, b.TextScore, b.Text); err != nil {
			return fmt.Errorf("replace ocr boxes for asset %d: insert: %w", assetID, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`update assets set ocr_status = ?, updated_at = unixepoch() where id = ?`,
		entity.TaskStatusOK, assetID); err != nil {
		return fmt.Errorf("set ocr status for asset %d: %w", assetID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace ocr boxes for asset %d: commit: %w", assetID, err)
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
	// OCRText: the text read from the asset, its boxes joined in reading
	// order, contains this text, ignoring ASCII case.
	OCRText *string
	// From/Until bound the capture time (unix epoch seconds), inclusive.
	From  *int64
	Until *int64
	// Cursor is the keyset pagination position.
	Cursor *AssetCursor
	// SimilarCursor is the keyset pagination position for similarity queries.
	SimilarCursor *SimilarCursor
	// ExcludeID leaves out the asset with this id, e.g. the source asset of
	// a find-similar query. Zero excludes nothing.
	ExcludeID int64
	Limit     int
}

// hasOnlineFileCond restricts to assets that still have at least one copy
// reachable on disk; an asset whose every file went offline is not part of the
// library any more.
const hasOnlineFileCond = `exists (
	select 1 from files f where f.asset_id = a.id and f.is_offline = 0
)`

// processedCond restricts to assets whose metadata and thumbnail steps have
// finished (ok or failed), so assets that were just inserted with only
// their checksum are not shown yet.
const processedCond = `a.metadata_status != 0 and a.thumbnail_status != 0`

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
// capture-time range across all non-deleted, processed assets that are
// still online.
func (r *Asset) GetFacets(ctx context.Context) (Facets, error) {
	var f Facets

	if err := r.db.QueryRowContext(ctx,
		`select count(*),
		        min(coalesce(a.date_time_local, a.date_time)),
		        max(coalesce(a.date_time_local, a.date_time))
		 from assets a
		 where a.deleted_at is null and `+processedCond+` and `+hasOnlineFileCond,
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
		   and ` + processedCond + ` and ` + hasOnlineFileCond + `
		 order by a.country`); err != nil {
		return f, fmt.Errorf("countries: %w", err)
	}
	if f.Cities, err = scanStrings(
		`select distinct a.city from assets a
		 where a.deleted_at is null and a.city is not null and a.city != ''
		   and ` + processedCond + ` and ` + hasOnlineFileCond + `
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
	conds := []string{"a.deleted_at is null", processedCond, hasOnlineFileCond}
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
	if q.OCRText != nil {
		// Joining the boxes lets a phrase match across boxes on one line.
		conds = append(conds, `(select group_concat(text, ' ')
			from (select text from asset_ocr_boxes
			      where asset_id = a.id
			      order by position)) like ? escape '\'`)
		args = append(args, "%"+likeEscaper.Replace(*q.OCRText)+"%")
	}
	if q.ExcludeID != 0 {
		conds = append(conds, "a.id != ?")
		args = append(args, q.ExcludeID)
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

// likeEscaper escapes the LIKE wildcards, with \ as the escape character.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

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
		conds = append(conds, "(distance > ? or (distance = ? and a.id > ?))")
		args = append(args, q.SimilarCursor.Distance, q.SimilarCursor.Distance, q.SimilarCursor.ID)
	}

	// The filters select the assets first, through the indexes on assets,
	// and the exact distance is computed for just those. SQLite resolves the
	// distance alias in where and order by.
	query := "select " + assetSelectColumns + `, vec1_cos_distance(?, e.embedding) as distance
		from assets a
		join asset_clip_embeddings e on e.asset_id = a.id
		where ` + whereClause(conds) + `
		order by distance asc, a.id asc
		limit ?`
	args = append([]any{embedding}, args...)
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
