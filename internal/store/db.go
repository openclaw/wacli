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

	return d.ensureSchema()
}
