package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestChatActivityMigrationRepairsOnlyKnownPlaceholders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		jid, text    string
		real, latest int64
	}{
		{"inflated", "", 100, 200},
		{"empty", "", 0, 200},
		{"literal", "(message)", 100, 200},
		{"unsynced", "", 100, 300},
	} {
		if err := db.UpsertChat(tc.jid, "dm", tc.jid, time.Unix(tc.latest, 0)); err != nil {
			t.Fatal(err)
		}
		if tc.real != 0 {
			if err := db.UpsertMessage(UpsertMessageParams{ChatJID: tc.jid, MsgID: "real", Timestamp: time.Unix(tc.real, 0), Text: "hello", DisplayText: "hello"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: tc.jid, MsgID: "system", Timestamp: time.Unix(200, 0), Text: tc.text, DisplayText: "(message)"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.sql.Exec("DELETE FROM schema_migrations WHERE version = 27"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for jid, want := range map[string]int64{"inflated": 100, "empty": 0, "literal": 200, "unsynced": 300} {
		chat, err := db.GetChat(jid)
		if err != nil {
			t.Fatal(err)
		}
		got := int64(0)
		if !chat.LastMessageTS.IsZero() {
			got = chat.LastMessageTS.Unix()
		}
		if got != want {
			t.Errorf("%s timestamp = %d, want %d", jid, got, want)
		}
		if _, err := db.GetMessage(jid, "system"); err != nil {
			t.Fatal(err)
		}
	}
}
