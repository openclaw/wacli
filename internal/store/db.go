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

	// Run init (which executes PRAGMAs) before chmod so the driver has
	// already created the file and any WAL/SHM sidecars.
	s := &DB{path: path, sql: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}

	// Restrict the DB file and its WAL/SHM sidecars to owner-only (0600)
	// regardless of umask. On multi-user systems the default sqlite3 driver
	// permissions (governed by umask, typically 0644) would make session data
	// world-readable. The sidecars contain in-progress write data and must be
	// treated with the same sensitivity as the main file (#50).
	if err := chmodDB(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

// chmodDB applies mode to path and, if they exist, to its WAL and SHM
// sidecar files (<path>-wal and <path>-shm).
func chmodDB(path string, mode os.FileMode) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		if err := os.Chmod(p, mode); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("chmod %s: %w", filepath.Base(p), err)
		}
	}
	return nil
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
