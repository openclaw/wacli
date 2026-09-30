//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDBFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("DB mode = %04o, want 0600", got)
	}
}
