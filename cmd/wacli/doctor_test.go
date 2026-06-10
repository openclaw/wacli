package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
)

func TestParseLockOwnerPID(t *testing.T) {
	tests := []struct {
		name string
		info string
		want int
	}{
		{name: "pid line", info: "pid=50394\nacquired_at=2026-04-05T12:30:11Z", want: 50394},
		{name: "trimmed pid", info: " pid= 42 ", want: 42},
		{name: "missing pid", info: "acquired_at=2026-04-05T12:30:11Z"},
		{name: "invalid pid", info: "pid=abc"},
		{name: "zero pid", info: "pid=0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLockOwnerPID(tc.info); got != tc.want {
				t.Fatalf("parseLockOwnerPID() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDoctorConnectionState(t *testing.T) {
	tests := []struct {
		name      string
		authed    bool
		connected bool
		lockHeld  bool
		connect   bool
		want      string
	}{
		{name: "connected wins", authed: true, connected: true, lockHeld: true, want: "connected"},
		{name: "locked paired session", authed: true, lockHeld: true, want: "locked_by_other_process"},
		{name: "connect requested stays disconnected", authed: true, lockHeld: true, connect: true, want: "disconnected"},
		{name: "plain disconnected", authed: true, want: "disconnected"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := doctorConnectionState(tc.authed, tc.connected, tc.lockHeld, tc.connect)
			if got != tc.want {
				t.Fatalf("doctorConnectionState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDoctorStoreStatsFromStoreStats(t *testing.T) {
	when := time.Date(2024, 4, 1, 12, 30, 0, 0, time.FixedZone("offset", 2*60*60))
	got := doctorStoreStatsFromStoreStats(store.StoreStats{
		Messages:      4,
		Chats:         3,
		Contacts:      2,
		Groups:        1,
		LastMessageTS: when.Unix(),
	})
	if got.Messages != 4 || got.Chats != 3 || got.Contacts != 2 || got.Groups != 1 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	if !got.StatsKnown {
		t.Fatal("StatsKnown = false, want true")
	}
	if got.LastSyncAt != "2024-04-01T10:30:00Z" {
		t.Fatalf("LastSyncAt = %q", got.LastSyncAt)
	}
}

func TestWriteDoctorReportIncludesLinkedJIDAndStats(t *testing.T) {
	var b bytes.Buffer
	writeDoctorReport(&b, doctorReport{
		StoreDir:        "/tmp/wacli",
		Authed:          true,
		LinkedJID:       "1234567890@s.whatsapp.net",
		ConnectionState: "disconnected",
		FTSEnabled:      true,
		Store: &doctorStoreStats{
			StatsKnown: true,
			Messages:   9,
			Chats:      8,
			Contacts:   7,
			Groups:     6,
			LastSyncAt: "2024-04-01T10:30:00Z",
		},
	})

	out := b.String()
	for _, want := range []string{
		"LINKED_JID",
		"1234567890@s.whatsapp.net",
		"MESSAGES",
		"9",
		"CHATS",
		"8",
		"CONTACTS",
		"7",
		"GROUPS",
		"6",
		"LAST_SYNC",
		"2024-04-01T10:30:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorReadOnlyDoesNotCreateStoreFiles(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	stdout := captureRootStdout(t, func() {
		if err := execute([]string{"--store", storeDir, "--read-only", "doctor"}); err != nil {
			t.Fatalf("execute doctor: %v", err)
		}
	})
	if !strings.Contains(stdout, "STORE") {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, name := range []string{"wacli.db", "session.db", "LOCK"} {
		if _, err := os.Stat(filepath.Join(storeDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s stat error = %v, want not exist", name, err)
		}
	}
}

func TestDoctorReadOnlyIgnoresStaleLockText(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lk.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	stdout := captureRootStdout(t, func() {
		if err := execute([]string{"--store", storeDir, "--read-only", "--json", "doctor"}); err != nil {
			t.Fatalf("execute doctor: %v", err)
		}
	})
	var got struct {
		Data doctorReport `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if got.Data.LockHeld {
		t.Fatalf("lock_held = true for stale lock text")
	}
}

func TestDoctorReportsLastActivityFromHeartbeat(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Create a minimal DB so doctor can open it.
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	db.Close()

	want := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(storeDir, "HEARTBEAT"), []byte(want.Format(time.RFC3339)), 0o644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	stdout := captureRootStdout(t, func() {
		if err := execute([]string{"--store", storeDir, "--read-only", "--json", "doctor"}); err != nil {
			t.Fatalf("execute doctor: %v", err)
		}
	})
	var got struct {
		Data struct {
			Store struct {
				LastActivityAt string `json:"last_activity_at"`
			} `json:"store"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if got.Data.Store.LastActivityAt != want.UTC().Format(time.RFC3339) {
		t.Fatalf("last_activity_at = %q, want %q", got.Data.Store.LastActivityAt, want.UTC().Format(time.RFC3339))
	}
}

func TestDoctorReportsLastActivityWhenStoreDBMissing(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	want := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(storeDir, "HEARTBEAT"), []byte(want.Format(time.RFC3339)), 0o644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}

	stdout := captureRootStdout(t, func() {
		if err := execute([]string{"--store", storeDir, "--read-only", "--json", "doctor"}); err != nil {
			t.Fatalf("execute doctor: %v", err)
		}
	})
	var got struct {
		Data struct {
			Store struct {
				LastActivityAt string `json:"last_activity_at"`
			} `json:"store"`
			StoreError string `json:"store_error"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if got.Data.Store.LastActivityAt != want.UTC().Format(time.RFC3339) {
		t.Fatalf("last_activity_at = %q, want %q", got.Data.Store.LastActivityAt, want.UTC().Format(time.RFC3339))
	}
	if got.Data.StoreError == "" {
		t.Fatal("expected missing store DB error")
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v\n%s", err, stdout)
	}
	data, ok := raw["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data object in %#v", raw)
	}
	storeObj, ok := data["store"].(map[string]any)
	if !ok {
		t.Fatalf("missing store object in %#v", data)
	}
	for _, name := range []string{"messages", "chats", "contacts", "groups"} {
		if _, ok := storeObj[name]; ok {
			t.Fatalf("heartbeat-only store object includes %q: %#v", name, storeObj)
		}
	}
}

func TestDoctorReportsCorruptStore(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "wacli.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	stdout := captureRootStdout(t, func() {
		if err := execute([]string{"--store", storeDir, "--read-only", "--json", "doctor"}); err != nil {
			t.Fatalf("execute doctor: %v", err)
		}
	})
	var got struct {
		Data doctorReport `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout)
	}
	if got.Data.StoreError == "" {
		t.Fatalf("store_error is empty for corrupt store: %s", stdout)
	}
}
