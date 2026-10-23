package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSemanticKindReadViewPages(t *testing.T) {
	db := mustOpen(t, filepath.Join(t.TempDir(), "wacli.db"))
	defer db.Close()
	chat := "123@lid"
	for i, kind := range []string{"poll", "contact", "event"} {
		seedOutgoing(t, db, chat, kind, time.Unix(int64(i+1), 0))
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: kind, Timestamp: time.Unix(int64(i+1), 0), Kind: kind, Text: "fixture " + kind}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetStarred(SetStarredParams{ChatJID: chat, MsgID: kind, Starred: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"list", "search", "export"} {
		t.Run(command, func(t *testing.T) {
			cursor := ""
			seen := 0
			for {
				var page MessagePage
				err := db.ReadView(func(view *DB) (err error) {
					if command == "search" {
						page, err = view.SearchMessagesPage(SearchMessagesParams{ChatJID: chat, Query: "fixture", Limit: 1}, cursor)
					} else {
						page, err = view.ListMessagesPage(command, ListMessagesParams{ChatJID: chat, Limit: 1}, cursor)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Messages) != 1 || page.Messages[0].Kind != page.Messages[0].MsgID {
					t.Fatalf("kind missing from page: %+v", page.Messages)
				}
				seen++
				if !page.Page.HasMore {
					break
				}
				cursor = *page.Page.NextCursor
			}
			if seen != 3 {
				t.Fatalf("got %d messages", seen)
			}
		})
	}
	if err := db.ReadView(func(view *DB) error {
		messages, err := view.ListStarredMessages(ListStarredMessagesParams{ChatJID: chat})
		if err != nil {
			return err
		}
		for _, message := range messages {
			if message.Kind != message.MsgID {
				t.Errorf("starred kind missing: %+v", message)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSemanticKindContextNeighbors(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db := mustOpen(t, path)
			chat := "123@lid"
			for i, kind := range []string{"poll", "", "contact"} {
				id := []string{"before", "target", "after"}[i]
				seedOutgoing(t, db, chat, id, time.Unix(int64(i+1), 0))
				if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: id, Timestamp: time.Unix(int64(i+1), 0), Kind: kind}); err != nil {
					t.Fatal(err)
				}
			}
			if legacy {
				if _, err := db.sql.Exec(`DROP TRIGGER retrieval_messages_update; ALTER TABLE messages DROP COLUMN kind; DELETE FROM schema_migrations WHERE version=35`); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			db, err := OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.ReadView(func(view *DB) error {
				messages, err := view.MessageContext(chat, "target", 1, 1)
				if err != nil {
					return err
				}
				if len(messages) != 3 {
					t.Fatalf("got %d messages", len(messages))
				}
				for i, want := range []string{"poll", "", "contact"} {
					if legacy {
						want = ""
					}
					if messages[i].Kind != want {
						t.Errorf("neighbor %d kind = %q, want %q", i, messages[i].Kind, want)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSemanticKindPurgeReplayAndIdentityMerge(t *testing.T) {
	for _, kind := range []string{"contact", "poll"} {
		for _, purgedChat := range []string{"123@lid", "456@s.whatsapp.net"} {
			t.Run(kind+"/"+purgedChat, func(t *testing.T) {
				db := mustOpen(t, filepath.Join(t.TempDir(), "wacli.db"))
				defer db.Close()
				original := UpsertMessageParams{ChatJID: purgedChat, MsgID: "fixture", Timestamp: time.Unix(1, 0), Kind: kind, Text: "fixture payload"}
				for _, chat := range []string{"123@lid", "456@s.whatsapp.net"} {
					seedOutgoing(t, db, chat, "fixture", original.Timestamp)
					p := original
					p.ChatJID = chat
					if err := db.UpsertMessage(p); err != nil {
						t.Fatal(err)
					}
				}
				assertPurged := func(stage, chat string) {
					t.Helper()
					m, err := db.GetMessage(chat, "fixture")
					if err != nil {
						t.Fatal(err)
					}
					if m.Kind != "" || m.PayloadPurgedAt == nil {
						t.Errorf("%s: show kind=%q, purged=%v", stage, m.Kind, m.PayloadPurgedAt)
					}
					b, err := json.Marshal(m)
					if err != nil || bytes.Contains(b, []byte(`"kind"`)) {
						t.Errorf("%s: JSON retains kind: %s (%v)", stage, b, err)
					}
					var stored sql.NullString
					if err := db.sql.QueryRow(`SELECT kind FROM messages WHERE chat_jid=? AND msg_id='fixture'`, chat).Scan(&stored); err != nil {
						t.Fatal(err)
					}
					if stored.Valid {
						t.Errorf("%s: SQLite kind=%q, want NULL", stage, stored.String)
					}
				}
				if err := db.MarkMessageDeletedForMe(purgedChat, "fixture", "", false, time.Unix(2, 0)); err != nil {
					t.Fatal(err)
				}
				if err := db.PurgeMessage(purgedChat, "fixture"); err != nil {
					t.Fatal(err)
				}
				assertPurged("purge", purgedChat)
				if err := db.UpsertMessage(original); err != nil {
					t.Fatal(err)
				}
				assertPurged("replay", purgedChat)
				if err := db.MigrateLIDToPN("123@lid", "456@s.whatsapp.net"); err != nil {
					t.Fatal(err)
				}
				assertPurged("identity merge", "456@s.whatsapp.net")
				original.ChatJID = "456@s.whatsapp.net"
				if err := db.UpsertMessage(original); err != nil {
					t.Fatal(err)
				}
				assertPurged("merged replay", original.ChatJID)
			})
		}
	}
}

// Optional old-binary compatibility check. The only store it can open is this
// test's disposable directory, and the command is explicitly read-only.
func TestSemanticKindReadableByPreviousBinary(t *testing.T) {
	bin := os.Getenv("WACLI_COMPAT_BIN")
	if bin == "" {
		t.Skip("set WACLI_COMPAT_BIN to the previous binary")
	}
	dir := t.TempDir()
	db := mustOpen(t, filepath.Join(dir, "wacli.db"))
	chat := "123@s.whatsapp.net"
	seedOutgoing(t, db, chat, "m1", time.Now())
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), Kind: "contact", Text: "fixture contact"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	output, err := exec.Command(bin, "--store", dir, "--read-only", "--json", "messages", "list", "--chat", chat, "--limit", "10").CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("fixture contact")) {
		t.Fatalf("old reader: %s (%v)", output, err)
	}
}

func TestSemanticKindMigrationAndLIDMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "123@lid"
	seedOutgoing(t, db, chat, "m1", time.Now())
	if _, err := db.sql.Exec(`DROP TRIGGER retrieval_messages_update; ALTER TABLE messages DROP COLUMN kind; DELETE FROM schema_migrations WHERE version = 35`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := createMessageChangeTriggers(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := ro.GetMessage(chat, "m1")
	if err != nil || old.Kind != "" {
		t.Fatalf("%+v: %v", old, err)
	}
	ro.Close()
	db = mustOpen(t, path)
	defer db.Close()
	if !db.kindEnabled {
		t.Fatal("migration did not add kind")
	}
	var trigger string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='retrieval_messages_update'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trigger, "old.kind IS NOT new.kind") {
		t.Fatal("kind does not invalidate retrieval cursors")
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), FromMe: true, Kind: "poll", Text: "Poll: test"}); err != nil {
		t.Fatal(err)
	}
	// An old writer omits kind; it must not erase the new classification.
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), FromMe: true, Text: "Poll: test"}); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateLIDToPN(chat, "456@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	msg, err := db.GetMessage("456@s.whatsapp.net", "m1")
	if err != nil || msg.Kind != "poll" || msg.MediaType != "" {
		t.Fatalf("%+v: %v", msg, err)
	}
	if err := db.MarkMessageRevoked("456@s.whatsapp.net", "m1"); err != nil {
		t.Fatal(err)
	}
	msg, err = db.GetMessage("456@s.whatsapp.net", "m1")
	if err != nil || msg.Kind != "revoked" {
		t.Fatalf("%+v: %v", msg, err)
	}
}
