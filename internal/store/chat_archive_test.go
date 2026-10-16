package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestArchiveIncomingConcurrentWriters(t *testing.T) {
	db := openTestDB(t)
	jid := "15550000001@s.whatsapp.net"
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(jid, "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchiveSetting(true, base.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for item := range 8 {
				id := fmt.Sprintf("incoming-%d-%d", worker, item)
				if err := db.UpsertMessage(UpsertMessageParams{ChatJID: jid, MsgID: id, Timestamp: base, Text: "synthetic", ArchiveIncoming: true}); err != nil {
					t.Errorf("concurrent incoming: %v", err)
					return
				}
			}
		})
	}
	workers.Wait()
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE chat_jid=?", jid); got != 64 {
		t.Fatalf("stored concurrent messages=%d, want 64", got)
	}
}

func TestArchiveIncomingIsAtomicAndOrdersSameSecond(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	jid := "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(jid, "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchiveSetting(true, base.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyChatArchive(jid, true, base.Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`CREATE TRIGGER fail_archive_incoming BEFORE UPDATE OF archive_incoming_ms ON chats BEGIN SELECT RAISE(FAIL,'injected boundary failure'); END`); err != nil {
		t.Fatal(err)
	}
	p := UpsertMessageParams{ChatJID: jid, MsgID: "incoming", Timestamp: base, Text: "synthetic", ArchiveIncoming: true}
	if err := db.UpsertMessage(p); err == nil {
		t.Fatal("expected boundary persistence failure")
	}
	if _, err := db.GetMessage(jid, p.MsgID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("message escaped rolled-back boundary: %v", err)
	}
	if _, err := db.sql.Exec(`DROP TRIGGER fail_archive_incoming`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`CREATE TRIGGER fail_incoming_message BEFORE INSERT ON messages BEGIN SELECT RAISE(FAIL,'injected message failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(p); err == nil {
		t.Fatal("expected message persistence failure")
	}
	if chat, err := db.GetChat(jid); err != nil || !chat.Archived {
		t.Fatalf("boundary escaped rolled-back message: %+v, %v", chat, err)
	}
	if _, err := db.sql.Exec(`DROP TRIGGER fail_incoming_message`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(p); err != nil {
		t.Fatal(err)
	}
	chat, err := db.GetChat(jid)
	if err != nil || chat.Archived {
		t.Fatalf("same-second incoming stayed archived: %+v, %v", chat, err)
	}
	if err := db.ApplyChatArchive(jid, true, base.Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	chat, err = db.GetChat(jid)
	if err != nil || chat.Archived {
		t.Fatalf("same-second replay undid incoming: %+v, %v", chat, err)
	}
	if err := db.ApplyChatArchive(jid, true, base.Add(800*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	// Simulate a second delivery that was classified before the first committed.
	if err := db.UpsertMessage(p); err != nil {
		t.Fatal(err)
	}
	chat, err = db.GetChat(jid)
	if err != nil || !chat.Archived {
		t.Fatalf("duplicate undid newer same-second archive: %+v, %v", chat, err)
	}
	p.MsgID = "next"
	p.Timestamp = base.Add(time.Second)
	if err := db.UpsertMessage(p); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyChatArchive(jid, true, p.Timestamp); err != nil {
		t.Fatal(err)
	}
	chat, err = db.GetChat(jid)
	if err != nil || !chat.Archived {
		t.Fatalf("later explicit action at equal precision lost: %+v, %v", chat, err)
	}
}

func TestChatArchiveUpgradeAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	jid := "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(jid, "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(jid, true); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE chats DROP COLUMN archive_action_ms",
		"ALTER TABLE chats DROP COLUMN archive_incoming_ms",
		"ALTER TABLE chats DROP COLUMN archive_cleared_ms",
		"ALTER TABLE chats DROP COLUMN archive_setting_min_ms",
		"DROP TABLE chat_archive_setting",
		"DELETE FROM schema_migrations WHERE name='ordered chat auto-unarchive'",
	} {
		if _, err := db.sql.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// An unmigrated read-only store retains the old public query contract.
	db, err = OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := db.ListChats("", 5); err != nil || len(rows) != 1 || !rows[0].Archived {
		t.Fatalf("read-only rows=%+v, %v", rows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if required, err := db.ChatArchiveRecoveryRequired(); err != nil || !required {
		t.Fatalf("upgrade recovery=%v, %v", required, err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: jid, MsgID: "incoming", Timestamp: base.Add(2 * time.Second), Text: "synthetic", ArchiveIncoming: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchiveSetting(true, base); err != nil {
		t.Fatal(err)
	}
	chat, err := db.GetChat(jid)
	if err != nil || !chat.Archived {
		t.Fatalf("unknown archive boundary changed: %+v, %v", chat, err)
	}
	if err := db.ApplyChatArchive(jid, true, base.Add(time.Second)); err != nil {
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
	if err := db.SetChatArchiveSetting(false, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyChatArchive(jid, true, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	chat, err = db.GetChat(jid)
	if err != nil || chat.Archived {
		t.Fatalf("replay after restart: %+v, %v", chat, err)
	}
}

func TestChatArchiveSettingUsesLaterEqualTimestampValue(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%t", enabled), func(t *testing.T) {
			db := openTestDB(t)
			jid := "15550000013@s.whatsapp.net"
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			if err := db.ApplyChatArchive(jid, true, base); err != nil {
				t.Fatal(err)
			}
			for _, value := range []bool{!enabled, enabled} {
				if err := db.SetChatArchiveSetting(value, base.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.UpsertMessage(UpsertMessageParams{ChatJID: jid, MsgID: "incoming", Timestamp: base.Add(2 * time.Second), Text: "synthetic", ArchiveIncoming: true}); err != nil {
				t.Fatal(err)
			}
			chat, err := db.GetChat(jid)
			if err != nil || chat.Archived == enabled {
				t.Fatalf("later equal-timestamp setting was ignored: %+v, %v", chat, err)
			}
		})
	}
}

func TestChatArchiveDeferredIncomingHonorsPayloadPurge(t *testing.T) {
	for _, purged := range []bool{false, true} {
		t.Run(fmt.Sprintf("purged=%t", purged), func(t *testing.T) {
			db := openTestDB(t)
			jid := "15550000016@s.whatsapp.net"
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			if err := db.SetChatArchiveSetting(true, base.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := db.ApplyChatArchive(jid, true, base); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertMessage(UpsertMessageParams{ChatJID: jid, MsgID: "incoming", Timestamp: base, Text: "synthetic", ArchiveIncoming: true}); err != nil {
				t.Fatal(err)
			}
			if purged {
				if err := db.MarkMessageDeletedForMe(jid, "incoming", jid, false, base); err != nil {
					t.Fatal(err)
				}
				if err := db.PurgeMessage(jid, "incoming"); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.ApplyChatArchive(jid, true, base.Add(500*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if err := db.ReconcileChatArchiveIncoming(jid, "incoming", base); err != nil {
				t.Fatal(err)
			}
			chat, err := db.GetChat(jid)
			if err != nil || chat.Archived != purged {
				t.Fatalf("deferred incoming ignored purge state: %+v, %v", chat, err)
			}
		})
	}
}

func TestChatArchiveCreatesMissingChatBeforeHistory(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprintf("archived=%t", archived), func(t *testing.T) {
			db := openTestDB(t)
			jid := "15550000012@s.whatsapp.net"
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			if err := db.ApplyChatArchive(jid, archived, base); err != nil {
				t.Fatal(err)
			}
			chat, err := db.GetChat(jid)
			if err != nil || chat.Archived != archived {
				t.Fatalf("missing chat archive was lost: %+v, %v", chat, err)
			}
			if err := db.UpsertChat(jid, "dm", "from history", base.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			chat, err = db.GetChat(jid)
			if err != nil || chat.Archived != archived {
				t.Fatalf("history overwrote archive: %+v, %v", chat, err)
			}
		})
	}
}

func TestChatArchiveIdentityMergeKeepsNewerBoundary(t *testing.T) {
	for _, incomingOnLID := range []bool{false, true} {
		t.Run(map[bool]string{false: "phone incoming", true: "lid incoming"}[incomingOnLID], func(t *testing.T) {
			db := openTestDB(t)
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			lid, phone := "123@lid", "15550000001@s.whatsapp.net"
			for _, jid := range []string{lid, phone} {
				if err := db.UpsertChat(jid, "dm", "Synthetic", base); err != nil {
					t.Fatal(err)
				}
				if err := db.ApplyChatArchive(jid, true, base.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.SetChatArchiveSetting(true, base); err != nil {
				t.Fatal(err)
			}
			incoming := phone
			if incomingOnLID {
				incoming = lid
			}
			if err := db.UpsertMessage(UpsertMessageParams{ChatJID: incoming, MsgID: "incoming", Timestamp: base.Add(2 * time.Second), Text: "synthetic", ArchiveIncoming: true}); err != nil {
				t.Fatal(err)
			}
			if err := db.MigrateLIDToPN(lid, phone); err != nil {
				t.Fatal(err)
			}
			if err := db.ApplyChatArchive(phone, true, base.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			chat, err := db.GetChat(phone)
			if err != nil || chat.Archived {
				t.Fatalf("merged archive=%+v, %v", chat, err)
			}
			if err := db.ApplyChatArchive(phone, true, base.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			chat, err = db.GetChat(phone)
			if err != nil || !chat.Archived {
				t.Fatalf("newer archive=%+v, %v", chat, err)
			}
		})
	}
}

func TestChatArchiveIdentityMergePreservesUnrecoveredArchive(t *testing.T) {
	db := openTestDB(t)
	lid, phone := "123@lid", "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(lid, "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(lid, true); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateLIDToPN(lid, phone); err != nil {
		t.Fatal(err)
	}
	chat, err := db.GetChat(phone)
	if err != nil || !chat.Archived {
		t.Fatalf("unrecovered archive lost: %+v, %v", chat, err)
	}
	if required, err := db.ChatArchiveRecoveryRequired(); err != nil || !required {
		t.Fatalf("missing upgrade recovery=%v, %v", required, err)
	}
}
