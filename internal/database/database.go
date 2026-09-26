package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// vec1Library is the path of the vec1 extension shared library, set by Open.
// The extension must be attached to every connection in the pool, which the
// connect hook of the registered driver does.
var vec1Library string

func init() {
	sql.Register("sqlite3-vec1", &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			if vec1Library == "" {
				return fmt.Errorf("vec1 extension library not configured")
			}
			return conn.LoadExtension(vec1Library, "sqlite3_extension_init")
		},
	})
}

// Open opens (creating if needed) the SQLite database under the library's
// .imchlite directory. vec1Library is the path of the vec1 extension shared
// library to load on every connection.
func Open(libraryLocation, vec1LibraryPath string) (*sql.DB, error) {
	vec1Library = vec1LibraryPath
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
		RawQuery: "_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_synchronous=NORMAL",
	}).String()

	db, err := sql.Open("sqlite3-vec1", dsn)
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
