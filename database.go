package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func openDatabase(libraryLocation string) (*sql.DB, error) {
	dir := filepath.Join(libraryLocation, ".imchlite")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create library dir: %w", err)
	}

	dbPath := filepath.Join(dir, "imchlite.db")

	// 1. Resolve absolute path to guarantee uniform cross-platform URI formatting
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("absolute path: %w", err)
	}

	// 2. Convert Windows backslashes to forward slashes for the URI
	uriPath := filepath.ToSlash(absPath)

	// 3. A valid file URI path must start with a slash.
	// On Windows, absolute paths (like C:/...) do not, so we prepend one.
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}

	dsn := (&url.URL{
		Scheme: "file",
		// Using 'Path' guarantees automatic URL escaping for characters 
		// like '#' or '?' so they aren't misread by SQLite.
		Path:     uriPath, 
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