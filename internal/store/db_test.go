package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenUsesExactPercentEscapedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store%3fprod%23one", "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.UpsertChat("123@s.whatsapp.net", "direct", "Exact", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat exact db path: %v", err)
	}

	roDB, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()
	count, err := roDB.CountChats()
	if err != nil {
		t.Fatalf("CountChats: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountChats = %d, want exact DB row", count)
	}
}

func TestOpenReadOnlyDoesNotCreateWALSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNoSQLiteSidecars(t, path)

	roDB, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if err := roDB.Close(); err != nil {
		t.Fatalf("Close read-only: %v", err)
	}
	assertNoSQLiteSidecars(t, path)
}

func TestOpenReadOnlyReadsLiveWALSidecars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.UpsertChat("123@s.whatsapp.net", "direct", "Live", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	assertSQLiteSidecarsExist(t, path)

	roDB, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer roDB.Close()
	count, err := roDB.CountChats()
	if err != nil {
		t.Fatalf("CountChats: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountChats = %d, want live WAL row", count)
	}
}

func TestOpenReadOnlyUsesLockingForAnySQLiteSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile db: %v", err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		sidecar := path + suffix
		if err := os.WriteFile(sidecar, []byte("sidecar"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", suffix, err)
		}
		if strings.Contains(sqliteURI(path, true), "immutable=1") {
			t.Fatalf("%s sidecar URI must preserve SQLite locking", suffix)
		}
		if err := os.Remove(sidecar); err != nil {
			t.Fatalf("Remove %s: %v", suffix, err)
		}
	}
	if !strings.Contains(sqliteURI(path, true), "immutable=1") {
		t.Fatalf("clean read-only URI must use immutable mode")
	}
}

func assertNoSQLiteSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s stat error = %v, want not exist", path+suffix, err)
		}
	}
}

func assertSQLiteSidecarsExist(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("%s stat error = %v, want exist", path+suffix, err)
		}
	}
}
