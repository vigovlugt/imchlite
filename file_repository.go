package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// fileRepository owns the database pool for file persistence.
type fileRepository struct {
	db *sql.DB
}

// IndexState is a file's persisted state as seen by the indexer.
type IndexState = File

// NewFile is the payload for inserting or updating a file row.
type NewFile struct {
	AssetID   *int64
	Path      string
	Inode     int64
	Size      int64
	MtimeS    int64
	MtimeNs   int64
	IsOffline bool
}

// NewFileRepository creates a fileRepository backed by the given pool.
func NewFileRepository(db *sql.DB) *fileRepository {
	return &fileRepository{db: db}
}

// getAll returns all files.
func (r *fileRepository) getAll(ctx context.Context) ([]File, error) {
	rows, err := r.db.QueryContext(ctx,
		`select id, asset_id, path, inode, size, mtime_s, mtime_ns, is_offline, created_at, updated_at from files`)
	if err != nil {
		return nil, fmt.Errorf("get all files: %w", err)
	}
	defer rows.Close()

	files := []File{}
	for rows.Next() {
		var f File
		if err := rows.Scan(
			&f.ID, &f.AssetID, &f.Path, &f.Inode, &f.Size, &f.MtimeS,
			&f.MtimeNs, &f.IsOffline, &f.CreatedAt, &f.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate files: %w", err)
	}
	return files, nil
}

// markOffline flags the given file ids as no longer reachable on disk.
func (r *fileRepository) markOffline(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("update files set is_offline = 1, updated_at = unixepoch() where id in (")
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("?")
		args = append(args, id)
	}
	sb.WriteString(")")

	if _, err := r.db.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("mark %d files offline: %w", len(ids), err)
	}
	return nil
}

// insert adds a new file row.
func (r *fileRepository) insert(ctx context.Context, f NewFile) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`insert into files (asset_id, path, inode, size, mtime_s, mtime_ns, is_offline)
		 values (?, ?, ?, ?, ?, ?, ?)`,
		f.AssetID, f.Path, f.Inode, f.Size, f.MtimeS, f.MtimeNs, f.IsOffline)
	if err != nil {
		return 0, fmt.Errorf("insert file %s: %w", f.Path, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert file %s: last insert id: %w", f.Path, err)
	}
	return id, nil
}

// upsert inserts a file row, or replaces its filesystem identity and asset
// link if a row already exists for the path. Returns the row id.
func (r *fileRepository) upsert(ctx context.Context, f NewFile) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`insert into files (asset_id, path, inode, size, mtime_s, mtime_ns, is_offline)
		 values (?, ?, ?, ?, ?, ?, ?)
		 on conflict (path) do update set
		    asset_id = excluded.asset_id,
		    inode = excluded.inode,
		    size = excluded.size,
		    mtime_s = excluded.mtime_s,
		    mtime_ns = excluded.mtime_ns,
		    is_offline = excluded.is_offline,
		    updated_at = unixepoch()`,
		f.AssetID, f.Path, f.Inode, f.Size, f.MtimeS, f.MtimeNs, f.IsOffline)
	if err != nil {
		return 0, fmt.Errorf("upsert file %s: %w", f.Path, err)
	}
	return res.LastInsertId()
}

// updateStat replaces the filesystem identity of an existing file row, keeping
// its asset link intact.
func (r *fileRepository) updateStat(ctx context.Context, id int64, f NewFile) error {
	if _, err := r.db.ExecContext(ctx,
		`update files set asset_id = ?, path = ?, inode = ?, size = ?, mtime_s = ?, mtime_ns = ?,
		        updated_at = unixepoch()
		 where id = ?`,
		f.AssetID, f.Path, f.Inode, f.Size, f.MtimeS, f.MtimeNs, id); err != nil {
		return fmt.Errorf("update stat for file %d: %w", id, err)
	}
	return nil
}
