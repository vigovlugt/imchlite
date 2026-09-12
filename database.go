package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

func openDatabase(libraryLocation string) (*sql.DB, error) {
	dir := filepath.Join(libraryLocation, ".imchlite")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create library dir: %w", err)
	}

	dsn := "file:" + filepath.Join(dir, "imchlite.db") +
		"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"

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
