package main

import (
	"context"
	"database/sql"
	"fmt"
)

type migration struct {
	version int64
	up      func(*sql.Tx) error
}

var migrations = []migration{
	{version: 1, up: migration001},
}

func migration001(tx *sql.Tx) error {
	statements := []string{
		`create table if not exists assets (
		    id integer primary key autoincrement,

		    -- content identity: the hash of the file's bytes
		    checksum blob not null unique,
		    mime_type text,

		    -- AssetType: 0 = image, 1 = video
		    type integer not null,

		    -- timestamps (unix epoch seconds)
		    file_created_at integer,
		    file_modified_at integer not null,
		    -- wall-clock capture time pinned to UTC (no zone info); null when
		    -- only a UTC instant is known
		    date_time_local integer,
		    -- true capture instant in UTC
		    date_time integer,

		    time_zone text,
		    latitude real,
		    longitude real,
		    city text,
		    country text,

		    -- media dimensions
		    width integer,
		    height integer,
		    duration_ms integer,
		    orientation integer,

		    -- ThumbnailStatus: 0 = ok, 1 = failed
		    thumbnail_status integer not null default 0,

		    -- user state
		    is_favorite integer not null default 0,
		    deleted_at integer,

		    created_at integer not null default (unixepoch()),
		    updated_at integer not null default (unixepoch())
		)`,
		`create index if not exists assets_capture_time_idx on assets (coalesce(date_time_local, date_time))`,
		`create table if not exists files (
		    id integer primary key autoincrement,

		    -- the logical content this file holds a copy of
		    -- null until the asset's checksum has been computed
		    asset_id integer references assets (id) on delete cascade,

		    -- filesystem identity (path is relative to the library root)
		    path text not null unique,
		    inode integer not null,
		    size integer not null,
		    mtime_s integer not null,
		    mtime_ns integer not null,

		    -- true once the file is no longer reachable on disk
		    is_offline integer not null default 0,

		    created_at integer not null default (unixepoch()),
		    updated_at integer not null default (unixepoch())
		)`,
		`create index if not exists files_asset_id_idx on files (asset_id)`,
		`create index if not exists files_inode_idx on files (inode)`,
	}

	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("migration001: %w", err)
		}
	}
	return nil
}

// initMigrations runs any pending migrations on the database.
func initMigrations(ctx context.Context, db *sql.DB) error {
	if err := ensureSchemaVersionTable(ctx, db); err != nil {
		return err
	}

	version, err := currentVersion(ctx, db)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		if m.version != version+1 {
			return fmt.Errorf("migration gap: have %d, next is %d", version, m.version)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}

		if err := m.up(tx); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"update schema_version set version = ? where singleton = 1", m.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}

		version = m.version
	}
	return nil
}

func ensureSchemaVersionTable(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`create table if not exists schema_version (
		    singleton integer primary key check (singleton = 1),
		    version integer not null default 0
		)`,
		`insert or ignore into schema_version (singleton, version) values (1, 0)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure schema_version: %w", err)
		}
	}
	return nil
}

func currentVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var version int64
	err := db.QueryRowContext(ctx,
		"select version from schema_version where singleton = 1").Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return version, err
}
