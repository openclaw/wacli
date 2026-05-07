package main

import (
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func TestHistoryCoverageCommandListsReadyAndBlockedChats(t *testing.T) {
	storeDir := t.TempDir()
	db, err := store.Open(storeDir + "/wacli.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat("ready@s.whatsapp.net", "dm", "Ready", base); err != nil {
		t.Fatalf("UpsertChat ready: %v", err)
	}
	if err := db.UpsertChat("blocked@s.whatsapp.net", "dm", "Blocked", base); err != nil {
		t.Fatalf("UpsertChat blocked: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   "ready@s.whatsapp.net",
		MsgID:     "m1",
		Timestamp: base,
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	_ = db.Close()

	cmd := newHistoryCoverageCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	cmd.SetArgs([]string{"--include-blocked"})
	raw := captureRootStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})
	if !strings.Contains(raw, "Ready") || !strings.Contains(raw, "Blocked") || !strings.Contains(raw, "no_local_anchor") {
		t.Fatalf("coverage output missing expected rows: %q", raw)
	}
}

func TestHistoryFillRequiresDryRun(t *testing.T) {
	cmd := newHistoryFillCmd(&rootFlags{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("expected --dry-run error, got %v", err)
	}
}

func TestHistoryFillDryRunSelectsReadyChats(t *testing.T) {
	storeDir := t.TempDir()
	db, err := store.Open(storeDir + "/wacli.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	base := time.Date(2024, 6, 2, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat("ready@s.whatsapp.net", "dm", "Ready", base); err != nil {
		t.Fatalf("UpsertChat ready: %v", err)
	}
	if err := db.UpsertChat("blocked@s.whatsapp.net", "dm", "Blocked", base); err != nil {
		t.Fatalf("UpsertChat blocked: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   "ready@s.whatsapp.net",
		MsgID:     "m1",
		Timestamp: base,
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	_ = db.Close()

	cmd := newHistoryFillCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	cmd.SetArgs([]string{"--dry-run"})
	raw := captureRootStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})
	if !strings.Contains(raw, "Selected 1 chats") || !strings.Contains(raw, "yes") || !strings.Contains(raw, "no") {
		t.Fatalf("dry-run output missing selection markers: %q", raw)
	}
}
