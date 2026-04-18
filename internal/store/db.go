package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	path       string
	sql        *sql.DB
	ftsEnabled bool
}

func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("db path is required")
	}
	// Reject paths that could inject SQLite URI parameters (#59).
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("db path must not contain '?' or '#'")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on&_busy_timeout=5000", path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	s := &DB{path: path, sql: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

func (d *DB) init() error {
	// Pragmas: keep consistent for writers/readers.
	_, _ = d.sql.Exec("PRAGMA journal_mode=WAL;")
	_, _ = d.sql.Exec("PRAGMA synchronous=NORMAL;")
	_, _ = d.sql.Exec("PRAGMA temp_store=MEMORY;")
	_, _ = d.sql.Exec("PRAGMA foreign_keys=ON;")

	if err := d.ensureSchema(); err != nil {
		return err
	}

	// Detect FTS5 availability independently of migration state.
	// The migration sets ftsEnabled only on first run; subsequent
	// opens skip the migration and leave ftsEnabled as false.
	if !d.ftsEnabled {
		if ok, _ := d.tableExists("messages_fts"); ok {
			var n int
			if err := d.sql.QueryRow("SELECT 1 FROM messages_fts LIMIT 1").Scan(&n); err == nil {
				d.ftsEnabled = true
			}
		}
	}

	return nil
}
