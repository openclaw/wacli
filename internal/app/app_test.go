package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDirPermissions(t *testing.T) {
	parent := t.TempDir()
	storeDir := filepath.Join(parent, "wacli-store")

	a, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	info, err := os.Stat(storeDir)
	if err != nil {
		t.Fatalf("Stat store dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected store dir permissions 0700, got %04o", perm)
	}

	// The wacli.db file inside should be 0600.
	dbPath := filepath.Join(storeDir, "wacli.db")
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat wacli.db: %v", err)
	}
	if perm := dbInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected wacli.db permissions 0600, got %04o", perm)
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}
