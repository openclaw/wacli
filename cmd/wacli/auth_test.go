package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthStatusPayloadIncludesLinkedJID(t *testing.T) {
	got := authStatusPayload(true, "1234567890@s.whatsapp.net")
	if got["authenticated"] != true {
		t.Fatalf("authenticated = %v", got["authenticated"])
	}
	if got["linked_jid"] != "1234567890@s.whatsapp.net" {
		t.Fatalf("linked_jid = %v", got["linked_jid"])
	}
	if got["phone"] != "1234567890" {
		t.Fatalf("phone = %v", got["phone"])
	}
}

func TestAuthStatusPayloadOmitsLinkedJIDWhenUnauthed(t *testing.T) {
	got := authStatusPayload(false, "1234567890@s.whatsapp.net")
	if _, ok := got["linked_jid"]; ok {
		t.Fatalf("linked_jid should be omitted: %+v", got)
	}
	if _, ok := got["phone"]; ok {
		t.Fatalf("phone should be omitted: %+v", got)
	}
}

func TestWriteAuthStatus(t *testing.T) {
	tests := []struct {
		name      string
		authed    bool
		linkedJID string
		want      string
	}{
		{name: "linked", authed: true, linkedJID: "1234567890@s.whatsapp.net", want: "Authenticated as 1234567890@s.whatsapp.net"},
		{name: "authed no jid", authed: true, want: "Authenticated."},
		{name: "not authed", want: "Not authenticated. Run `wacli auth`."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			writeAuthStatus(&b, tc.authed, tc.linkedJID)
			if got := strings.TrimSpace(b.String()); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthStatusReadOnlyDoesNotCreateStoreFiles(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	stdout := captureRootStdout(t, func() {
		if err := execute([]string{"--store", storeDir, "--read-only", "auth", "status"}); err != nil {
			t.Fatalf("execute auth status: %v", err)
		}
	})
	if !strings.Contains(stdout, "Not authenticated") {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, name := range []string{"session.db", "wacli.db", "LOCK"} {
		if _, err := os.Stat(filepath.Join(storeDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s stat error = %v, want not exist", name, err)
		}
	}
}

func TestReadOnlyAuthStatusNormalizesDeviceJID(t *testing.T) {
	storeDir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(storeDir, "session.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		t.Fatalf("Enable WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE whatsmeow_device (jid TEXT)`); err != nil {
		_ = db.Close()
		t.Fatalf("Create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO whatsmeow_device (jid) VALUES (?)`, "15551234567:23@s.whatsapp.net"); err != nil {
		_ = db.Close()
		t.Fatalf("Insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNoAuthSQLiteSidecars(t, filepath.Join(storeDir, "session.db"))

	authed, linkedJID, err := readOnlyAuthStatus(storeDir)
	if err != nil {
		t.Fatalf("readOnlyAuthStatus: %v", err)
	}
	if !authed || linkedJID != "15551234567@s.whatsapp.net" {
		t.Fatalf("status = %v, %q; want normalized linked JID", authed, linkedJID)
	}
	if !strings.Contains(readOnlySessionSQLiteURI(filepath.Join(storeDir, "session.db")), "immutable=1") {
		t.Fatalf("clean session read-only URI must avoid creating WAL sidecars")
	}
	assertNoAuthSQLiteSidecars(t, filepath.Join(storeDir, "session.db"))
}

func TestReadOnlyAuthStatusReadsLiveWALSidecars(t *testing.T) {
	storeDir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(storeDir, "session.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("Enable WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE whatsmeow_device (jid TEXT)`); err != nil {
		t.Fatalf("Create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO whatsmeow_device (jid) VALUES (?)`, "15557654321:12@s.whatsapp.net"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	assertAuthSQLiteSidecarsExist(t, filepath.Join(storeDir, "session.db"))

	authed, linkedJID, err := readOnlyAuthStatus(storeDir)
	if err != nil {
		t.Fatalf("readOnlyAuthStatus: %v", err)
	}
	if !authed || linkedJID != "15557654321@s.whatsapp.net" {
		t.Fatalf("status = %v, %q; want live WAL linked JID", authed, linkedJID)
	}
}

func TestReadOnlySessionSQLiteURIWindowsPath(t *testing.T) {
	uri := readOnlySessionSQLiteURI(`C:\Users\me\.wacli\session.db`)
	if !strings.HasPrefix(uri, "file:///C:/Users/me/.wacli/session.db?") {
		t.Fatalf("readOnlySessionSQLiteURI = %q, want absolute Windows file URI", uri)
	}
	if !strings.Contains(uri, "mode=ro") {
		t.Fatalf("readOnlySessionSQLiteURI = %q, want read-only query parameter", uri)
	}
}

func TestReadOnlySessionSQLiteURIUsesLockingForLiveState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile db: %v", err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		sidecar := path + suffix
		if err := os.WriteFile(sidecar, []byte("sidecar"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", suffix, err)
		}
		if strings.Contains(readOnlySessionSQLiteURI(path), "immutable=1") {
			t.Fatalf("%s sidecar URI must preserve SQLite locking", suffix)
		}
		if err := os.Remove(sidecar); err != nil {
			t.Fatalf("Remove %s: %v", suffix, err)
		}
	}
	if !strings.Contains(readOnlySessionSQLiteURI(path), "immutable=1") {
		t.Fatalf("clean session read-only URI must use immutable mode")
	}
}

func assertNoAuthSQLiteSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s stat error = %v, want not exist", path+suffix, err)
		}
	}
}

func assertAuthSQLiteSidecarsExist(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("%s stat error = %v, want exist", path+suffix, err)
		}
	}
}

func TestNormalizeAuthQRFormat(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "", want: "terminal"},
		{input: " TERMINAL ", want: "terminal"},
		{input: "text", want: "text"},
		{input: "png", wantErr: true},
	}
	for _, tc := range tests {
		got, err := normalizeAuthQRFormat(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("normalizeAuthQRFormat(%q) expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeAuthQRFormat(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeAuthQRFormat(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAuthQRWriterText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	authQRWriter("text", &stdout, &stderr, nil)("2@test-code")
	if got := strings.TrimSpace(stdout.String()); got != "2@test-code" {
		t.Fatalf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestNormalizePairPhone(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "", want: ""},
		{input: "+15551234567", want: "15551234567"},
		{input: "15551234567", want: "15551234567"},
		{input: "123@g.us", wantErr: true},
		{input: "123abc", wantErr: true},
	}
	for _, tc := range tests {
		got, err := normalizePairPhone(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("normalizePairPhone(%q) expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizePairPhone(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalizePairPhone(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAuthPairCodeWriter(t *testing.T) {
	var stderr bytes.Buffer
	writer := authPairCodeWriter("15551234567", &stderr, nil)
	if writer == nil {
		t.Fatal("expected writer")
	}
	writer("ABCD-1234")
	got := stderr.String()
	if !strings.Contains(got, "Pairing code for +15551234567: ABCD-1234") {
		t.Fatalf("stderr = %q", got)
	}
	if authPairCodeWriter("", &stderr, nil) != nil {
		t.Fatal("expected nil writer without phone")
	}
}

func TestAuthCommandExposesQRFormat(t *testing.T) {
	cmd := newAuthCmd(&rootFlags{})
	flag := cmd.Flags().Lookup("qr-format")
	if flag == nil {
		t.Fatal("expected --qr-format flag")
	}
	if flag.DefValue != "terminal" {
		t.Fatalf("qr-format default = %q", flag.DefValue)
	}
	if cmd.Flags().Lookup("phone") == nil {
		t.Fatal("expected --phone flag")
	}
}

func TestPhoneFromLinkedJID(t *testing.T) {
	if got := phoneFromLinkedJID("123@s.whatsapp.net"); got != "123" {
		t.Fatalf("phoneFromLinkedJID = %q", got)
	}
	if got := phoneFromLinkedJID("not-a-jid"); got != "" {
		t.Fatalf("phoneFromLinkedJID invalid = %q", got)
	}
}
