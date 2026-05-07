package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func TestGroupsPruneExposesSafetyFlags(t *testing.T) {
	cmd := newGroupsPruneCmd(&rootFlags{})
	for _, name := range []string{"days", "left-only", "include-active", "dry-run", "confirm"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
}

func TestGroupsPruneRejectsReadOnlyBeforeOpeningStore(t *testing.T) {
	cmd := newGroupsPruneCmd(&rootFlags{readOnly: true})
	cmd.SetArgs([]string{"--dry-run"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatalf("error = %v, want read-only", err)
	}
}

func TestGroupsPruneDryRunDoesNotDeleteOlderLeftGroups(t *testing.T) {
	storeDir := seedPruneStore(t)

	cmd := newGroupsPruneCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	cmd.SetArgs([]string{"--days", "180", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("groups prune dry-run: %v", err)
	}

	db := openPruneStore(t, storeDir)
	defer db.Close()
	if _, err := db.GetChat("old-left@g.us"); err != nil {
		t.Fatalf("old-left chat should survive dry-run: %v", err)
	}
	left, err := db.ListPrunableGroups(180, false)
	if err != nil {
		t.Fatalf("ListPrunableGroups: %v", err)
	}
	if got := len(left); got != 1 {
		t.Fatalf("dry-run deleted targets: got %d left, want 1", got)
	}
}

func TestGroupsPruneConfirmDeletesOnlyMatchingGroups(t *testing.T) {
	storeDir := seedPruneStore(t)

	cmd := newGroupsPruneCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	cmd.SetArgs([]string{"--days", "180", "--confirm"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("groups prune confirm: %v", err)
	}

	db := openPruneStore(t, storeDir)
	defer db.Close()
	if _, err := db.GetChat("old-left@g.us"); err == nil {
		t.Fatalf("old-left chat should be deleted")
	}
	for _, jid := range []string{"recent-left@g.us", "old-active@g.us"} {
		if _, err := db.GetChat(jid); err != nil {
			t.Fatalf("%s chat should survive: %v", jid, err)
		}
	}
}

func seedPruneStore(t *testing.T) string {
	t.Helper()
	storeDir := t.TempDir()
	db := openPruneStore(t, storeDir)
	defer db.Close()

	now := time.Now().UTC()
	created := now.AddDate(0, 0, -400)
	oldLeft := now.AddDate(0, 0, -200)
	recentLeft := now.AddDate(0, 0, -30)
	oldActive := now.AddDate(0, 0, -220)
	for _, tc := range []struct {
		jid    string
		name   string
		lastTS time.Time
		leftAt time.Time
	}{
		{"old-left@g.us", "Old Left", oldLeft, oldLeft},
		{"recent-left@g.us", "Recent Left", recentLeft, recentLeft},
		{"old-active@g.us", "Old Active", oldActive, time.Time{}},
	} {
		if err := db.UpsertGroup(tc.jid, tc.name, "owner@s.whatsapp.net", created); err != nil {
			t.Fatalf("UpsertGroup %s: %v", tc.jid, err)
		}
		if err := db.UpsertChat(tc.jid, "group", tc.name, tc.lastTS); err != nil {
			t.Fatalf("UpsertChat %s: %v", tc.jid, err)
		}
		if !tc.leftAt.IsZero() {
			if err := db.MarkGroupLeft(tc.jid, tc.leftAt); err != nil {
				t.Fatalf("MarkGroupLeft %s: %v", tc.jid, err)
			}
		}
	}
	return storeDir
}

func openPruneStore(t *testing.T, storeDir string) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}
