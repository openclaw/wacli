package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func TestGroupsParticipantsListCommand(t *testing.T) {
	storeDir := t.TempDir()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}

	gid := "123456789@g.us"
	if err := db.UpsertGroup(gid, "Budget Group", "owner@s.whatsapp.net", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceGroupParticipants(gid, []store.GroupParticipant{
		{GroupJID: gid, UserJID: "alice@s.whatsapp.net", Role: "admin"},
		{GroupJID: gid, UserJID: "bob@s.whatsapp.net", Role: "member"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Test 1: JSON output in read-only mode without network/auth
	{
		cmd := newGroupsParticipantsListCmd(&rootFlags{storeDir: storeDir, readOnly: true, asJSON: true})
		cmd.SetArgs([]string{"--jid", gid})
		stdout := captureRootStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
		})

		var res struct {
			Success bool                     `json:"success"`
			Data    []store.GroupParticipant `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			t.Fatalf("unmarshal stdout %q: %v", stdout, err)
		}
		if !res.Success || len(res.Data) != 2 {
			t.Fatalf("unexpected JSON result: %+v", res)
		}
		if res.Data[0].UserJID != "alice@s.whatsapp.net" || res.Data[0].Role != "admin" {
			t.Errorf("row 0 = %+v, want alice as admin", res.Data[0])
		}
		if res.Data[1].UserJID != "bob@s.whatsapp.net" || res.Data[1].Role != "member" {
			t.Errorf("row 1 = %+v, want bob as member", res.Data[1])
		}
	}

	// Test 2: Table output
	{
		cmd := newGroupsParticipantsListCmd(&rootFlags{storeDir: storeDir, readOnly: true, asJSON: false})
		cmd.SetArgs([]string{"--jid", gid})
		stdout := captureRootStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute table: %v", err)
			}
		})

		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) != 3 { // Header + 2 rows
			t.Fatalf("expected 3 lines in table output, got %d:\n%s", len(lines), stdout)
		}
		if !strings.Contains(lines[0], "USER JID") || !strings.Contains(lines[0], "ROLE") {
			t.Errorf("header = %q", lines[0])
		}
		if !strings.Contains(lines[1], "alice@s.whatsapp.net") || !strings.Contains(lines[1], "admin") {
			t.Errorf("row 1 = %q", lines[1])
		}
		if !strings.Contains(lines[2], "bob@s.whatsapp.net") || !strings.Contains(lines[2], "member") {
			t.Errorf("row 2 = %q", lines[2])
		}
	}

	// Test 3: Missing --jid flag errors cleanly
	{
		cmd := newGroupsParticipantsListCmd(&rootFlags{storeDir: storeDir, readOnly: true})
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "--jid is required") {
			t.Fatalf("expected --jid is required error, got: %v", err)
		}
	}
}
