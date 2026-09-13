package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

func openDatabase(libraryLocation string) (*sql.DB, error) {
	dir := filepath.Join(libraryLocation, ".imchlite")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create library dir: %w", err)
	}

	dsn := (&url.URL{
		Scheme: "file",
		// URL escaping is required so characters like '#' or '?' in the
		// library path are not misread as URI fragment/query separators.
		Path:     filepath.Join(dir, "imchlite.db"),
		RawQuery: "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on",
	}).String()

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	// sql.DB is a connection pool; keep it small since SQLite
	// serializes writes anyway.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}
