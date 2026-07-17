package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestOpenWAConcurrentInitialization(t *testing.T) {
	a := newTestApp(t)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.OpenWA()
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("OpenWA: %v", err)
		}
	}
	if a.WA() == nil {
		t.Fatal("WA client was not initialized")
	}
}

func TestNewReadOnlyDoesNotCreateStoreDir(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "missing")
	_, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err == nil {
		t.Fatal("New read-only succeeded without an existing DB")
	}
	if _, statErr := os.Stat(storeDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("store dir stat err = %v, want not exist", statErr)
	}
}

func TestNewReadOnlyUsesReadOnlyStore(t *testing.T) {
	storeDir := t.TempDir()
	writer, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}
	if err := writer.db.UpsertChat("123@s.whatsapp.net", "dm", "Alice", time.Now().UTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	writer.Close()

	assertNoAppSQLiteSidecars(t, filepath.Join(storeDir, "wacli.db"))

	reader, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()
	count, err := reader.db.CountChats()
	if err != nil {
		t.Fatalf("CountChats: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountChats = %d, want 1", count)
	}
	if err := reader.db.UpsertChat("456@s.whatsapp.net", "dm", "Bob", time.Now().UTC()); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("read-only UpsertChat err = %v, want readonly error", err)
	}

	assertNoAppSQLiteSidecars(t, filepath.Join(storeDir, "wacli.db"))
}

func TestOpenWARejectsReadOnlyWithoutCreatingSession(t *testing.T) {
	storeDir := t.TempDir()
	writer, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}
	writer.Close()

	reader, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()

	if err := reader.OpenWA(); err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatalf("OpenWA err = %v, want read-only error", err)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "session.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session.db stat err = %v, want not exist", err)
	}
}

func TestReadOnlyLocalResolverReadsSessionLIDMap(t *testing.T) {
	storeDir := t.TempDir()
	writer, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}
	writer.Close()

	sessionPath := filepath.Join(storeDir, "session.db")
	writeSessionLIDMap(t, sessionPath, "999123456789", "15551234567")

	reader, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()

	resolver, err := reader.LocalResolver()
	if err != nil {
		t.Fatalf("LocalResolver: %v", err)
	}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	if got := resolver.ResolvePNToLID(context.Background(), pn); got != lid {
		t.Fatalf("ResolvePNToLID = %s, want %s", got, lid)
	}
	if got := resolver.ResolveLIDToPN(context.Background(), lid); got != pn {
		t.Fatalf("ResolveLIDToPN = %s, want %s", got, pn)
	}
	assertNoAppSQLiteSidecars(t, sessionPath)
}

func TestReadOnlySessionURIEscapesPathDelimiters(t *testing.T) {
	uri := readOnlySessionURI(filepath.Join(t.TempDir(), "session?prod#1.db"))
	if strings.Contains(uri, "session?prod#1.db") {
		t.Fatalf("readOnlySessionURI = %q, want escaped path delimiters", uri)
	}
	if !strings.Contains(uri, "session%3Fprod%231.db") {
		t.Fatalf("readOnlySessionURI = %q, want escaped session filename", uri)
	}
	if !strings.Contains(uri, "?_foreign_keys=on") {
		t.Fatalf("readOnlySessionURI = %q, want SQLite query params", uri)
	}
}

func TestReadOnlySessionURIWindowsPath(t *testing.T) {
	uri := readOnlySessionURI(`C:\Users\me\.wacli\session.db`)
	if !strings.HasPrefix(uri, "file:///C:/Users/me/.wacli/session.db?") {
		t.Fatalf("readOnlySessionURI = %q, want absolute Windows file URI", uri)
	}
	if !strings.Contains(uri, "mode=ro") {
		t.Fatalf("readOnlySessionURI = %q, want read-only query parameter", uri)
	}
}

func TestReadOnlyLocalResolverUsesExactPercentEscapedStorePath(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store%3fprod%23one")
	writer, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}
	writer.Close()

	sessionPath := filepath.Join(storeDir, "session.db")
	writeSessionLIDMap(t, sessionPath, "999123456789", "15551234567")

	reader, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()

	resolver, err := reader.LocalResolver()
	if err != nil {
		t.Fatalf("LocalResolver: %v", err)
	}
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	want := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	if got := resolver.ResolveLIDToPN(context.Background(), lid); got != want {
		t.Fatalf("ResolveLIDToPN = %s, want %s", got, want)
	}
}

func TestReadOnlyLocalResolverReadsOwnDeviceLID(t *testing.T) {
	storeDir := t.TempDir()
	writer, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}
	writer.Close()

	sessionPath := filepath.Join(storeDir, "session.db")
	writeSessionDeviceLID(t, sessionPath, "15551234567:23@s.whatsapp.net", "999123456789@lid")

	reader, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()

	resolver, err := reader.LocalResolver()
	if err != nil {
		t.Fatalf("LocalResolver: %v", err)
	}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	if got := resolver.ResolvePNToLID(context.Background(), pn); got != lid {
		t.Fatalf("ResolvePNToLID = %s, want %s", got, lid)
	}
	if got := resolver.ResolveLIDToPN(context.Background(), lid); got != pn {
		t.Fatalf("ResolveLIDToPN = %s, want %s", got, pn)
	}
	assertNoAppSQLiteSidecars(t, sessionPath)
}

func TestReadOnlyLocalResolverUsesRedactedPhoneName(t *testing.T) {
	storeDir := t.TempDir()
	writer, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}
	writer.Close()

	sessionPath := filepath.Join(storeDir, "session.db")
	writeSessionContact(t, sessionPath, "15551234567@s.whatsapp.net", "+1***4567")

	reader, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()

	resolver, err := reader.LocalResolver()
	if err != nil {
		t.Fatalf("LocalResolver: %v", err)
	}
	jid := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	if got := resolver.ResolveChatName(context.Background(), jid, ""); got != "+1***4567" {
		t.Fatalf("ResolveChatName = %q, want redacted phone", got)
	}
	assertNoAppSQLiteSidecars(t, sessionPath)
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

func writeSessionContact(t *testing.T, path, jid, redacted string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("Open session sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE whatsmeow_contacts (
			our_jid TEXT,
			their_jid TEXT,
			first_name TEXT,
			full_name TEXT,
			push_name TEXT,
			business_name TEXT,
			redacted_phone TEXT,
			PRIMARY KEY (our_jid, their_jid)
		);
		INSERT INTO whatsmeow_contacts (our_jid, their_jid, redacted_phone) VALUES (?, ?, ?);
	`, "me@s.whatsapp.net", jid, redacted)
	if err != nil {
		t.Fatalf("seed session contact: %v", err)
	}
}

func writeSessionDeviceLID(t *testing.T, path, pn, lid string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("Open session sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE whatsmeow_device (jid TEXT PRIMARY KEY, lid TEXT);
		INSERT INTO whatsmeow_device (jid, lid) VALUES (?, ?);
	`, pn, lid)
	if err != nil {
		t.Fatalf("seed session device LID: %v", err)
	}
}

func writeSessionLIDMap(t *testing.T, path, lid, pn string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("Open session sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL);
		INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?);
	`, lid, pn)
	if err != nil {
		t.Fatalf("seed session LID map: %v", err)
	}
}

func assertNoAppSQLiteSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		sidecar := path + suffix
		if _, err := os.Stat(sidecar); err == nil {
			t.Fatalf("unexpected SQLite sidecar %s", sidecar)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s: %v", sidecar, err)
		}
	}
}
