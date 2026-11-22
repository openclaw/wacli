package store

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestHistoryReadPositionSurvivesRestartAndOlderReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedUnreadChat(t, db, ownReadChat, 2, false)
	storeMsg(t, db, UpsertMessageParams{MsgID: "incoming", Timestamp: ownReadBase, Text: "question"})
	storeMsg(t, db, UpsertMessageParams{MsgID: "reply", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "answer"})
	if err := db.ClearChatUnreadThrough(ownReadChat, ownReadBase, []string{"incoming"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SetHistoryChatUnread(ownReadChat, 500, true); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, false, 0)
	// An old read must not erase a later manual marker, and neither should a
	// stale history snapshot. This only guards counters; it is not a new read.
	if err := db.SetChatUnread(ownReadChat, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetHistoryChatUnread(ownReadChat, 500, false); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, true, 0)
}

func TestHistoryReadPositionPreservesLaterSameSecondArrival(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 1, false)
	storeMsg(t, db, UpsertMessageParams{MsgID: "first", Timestamp: ownReadBase, Text: "first"})
	if err := db.ClearChatUnreadThrough(ownReadChat, ownReadBase, []string{"first"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetHistoryChatUnread(ownReadChat, 10, true); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, false, 0)
	storeMsg(t, db, UpsertMessageParams{MsgID: "later", Timestamp: ownReadBase, Text: "later"})
	if err := db.SetHistoryChatUnread(ownReadChat, 1, false); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, true, 1)
}

func TestHistoryWithoutReadEvidenceKeepsPhoneUnreadState(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 2, false)
	storeMsg(t, db, UpsertMessageParams{MsgID: "historical-own", Timestamp: ownReadBase, FromMe: true, Text: "history", SkipOwnReadMark: true})
	if err := db.SetHistoryChatUnread(ownReadChat, 9, false); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, true, 9)
	if err := db.SetHistoryChatUnread(ownReadChat, 0, true); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, true, 9)
	if err := db.SetHistoryChatUnread(ownReadChat, 0, false); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, false, 0)
}

func TestHistoryReadPositionFollowsLIDMigration(t *testing.T) {
	db := openTestDB(t)
	lid := "123@lid"
	seedUnreadChat(t, db, lid, 1, false)
	storeMsg(t, db, UpsertMessageParams{ChatJID: lid, MsgID: "incoming", Timestamp: ownReadBase, Text: "question"})
	storeMsg(t, db, UpsertMessageParams{ChatJID: lid, MsgID: "reply", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "answer"})
	if err := db.MigrateLIDToPN(lid, ownReadChat); err != nil {
		t.Fatal(err)
	}
	if err := db.SetHistoryChatUnread(ownReadChat, 100, true); err != nil {
		t.Fatal(err)
	}
	assertUnread(t, db, ownReadChat, false, 0)
}

func TestClearChatUnreadThroughCountsOnlyWhatAReadMissed(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	read := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	add := func(id string, offset time.Duration, apply func(*UpsertMessageParams)) {
		t.Helper()
		p := UpsertMessageParams{
			ChatJID:   chat,
			MsgID:     id,
			SenderJID: chat,
			Timestamp: read.Add(offset),
			Text:      id,
		}
		if apply != nil {
			apply(&p)
		}
		if err := db.UpsertMessage(p); err != nil {
			t.Fatalf("UpsertMessage %s: %v", id, err)
		}
	}
	add("before", -time.Minute, nil)
	add("at", 0, nil)
	add("after1", time.Minute, nil)
	add("after2", 2*time.Minute, nil)
	add("mine", 3*time.Minute, func(p *UpsertMessageParams) { p.FromMe = true })
	add("reaction", 4*time.Minute, func(p *UpsertMessageParams) {
		p.ReactionToID = "after1"
		p.ReactionEmoji = "👍"
	})
	add("revoked", 5*time.Minute, func(p *UpsertMessageParams) { p.Revoked = true })
	add("deleted", 6*time.Minute, func(p *UpsertMessageParams) { p.DeletedForMe = true })

	if err := db.SetChatUnreadCount(chat, 4); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearChatUnreadThrough(chat, read, []string{"at"}); err != nil {
		t.Fatal(err)
	}
	c, err := db.GetChat(chat)
	if err != nil {
		t.Fatal(err)
	}
	if c.UnreadCount != 2 || !c.Unread {
		t.Fatalf("after read = %+v, want two unread messages", c)
	}
	if err := db.ClearChatUnreadThrough(chat, read.Add(10*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearChatUnreadThrough(chat, read, []string{"at"}); err != nil {
		t.Fatal(err)
	}
	c, err = db.GetChat(chat)
	if err != nil {
		t.Fatal(err)
	}
	if c.UnreadCount != 0 || c.Unread {
		t.Fatalf("older replay restored read messages: %+v", c)
	}
	if err := db.ClearChatUnreadThrough("  ", read, nil); err == nil {
		t.Fatal("empty JID accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearChatUnreadThrough(chat, read, nil); err == nil {
		t.Fatal("closed DB error swallowed")
	}

}

func TestClearEmptyUnreadPreservesAnArrivalAfterReceiptSelection(t *testing.T) {
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	at := time.Unix(1700000000, 0)
	if err := db.UpsertChat(chat, "dm", "Chat", at); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatUnread(chat, true); err != nil {
		t.Fatal(err)
	}
	msgs, err := db.ReadReceiptCandidates(chat, 100)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("empty selection = %v, %v", msgs, err)
	}
	// A message can arrive between the selection snapshot and local completion.
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "arrival", Timestamp: at, Text: "new"}); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{0, 8} {
		if err := db.SetChatUnreadCount(chat, count); err != nil {
			t.Fatal(err)
		}
		if err := db.SetChatUnread(chat, true); err != nil {
			t.Fatal(err)
		}
		if err := db.ClearChatUnreadMarker(chat); err != nil {
			t.Fatal(err)
		}
		c, err := db.GetChat(chat)
		if err != nil || !c.Unread || c.UnreadCount != count {
			t.Fatalf("arrival lost (count %d): %+v, %v", count, c, err)
		}
	}
}

func TestClearChatUnreadThroughApproximateKeepsNoReadPosition(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertChat(ownReadChat, "dm", "Chat", ownReadBase); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatUnreadCount(ownReadChat, 2); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearChatUnreadThroughApproximate(ownReadChat, ownReadBase.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	var ts, row int64
	if err := db.sql.QueryRow(`SELECT read_through_ts, read_through_rowid FROM chats WHERE jid = ?`, ownReadChat).Scan(&ts, &row); err != nil {
		t.Fatal(err)
	}
	if ts != 0 || row != 0 {
		t.Fatalf("approximate read persisted position %d/%d", ts, row)
	}
	c, err := db.GetChat(ownReadChat)
	if err != nil || c.UnreadCount != 0 || c.Unread {
		t.Fatalf("approximate read did not clear unread: %+v, %v", c, err)
	}
}

func chatsSchema(t *testing.T, db *DB) string {
	t.Helper()
	rows, err := db.sql.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk FROM pragma_table_info('chats') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, typ, dflt string
		var notNull, pk int
		if err := rows.Scan(&name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprint(name, typ, notNull, dflt, pk))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out) // ALTER TABLE appends columns; order is not part of the shape.
	return strings.Join(out, "\n")
}

func TestReadPositionMigrationIsVersionedAndIdempotent(t *testing.T) {
	fresh, err := Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	want := chatsSchema(t, fresh)
	var recorded int
	if err := fresh.sql.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = 39 AND name = 'local chat read positions'`).Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("fresh store did not record migration 39: %d, %v", recorded, err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}

	// Fork builds recorded 34..38 for unrelated schema; this migration must
	// still run, and must tolerate a shape that is already present.
	for _, shapePresent := range []bool{true, false} {
		t.Run(fmt.Sprint("shapePresent=", shapePresent), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			statements := []string{`DELETE FROM schema_migrations WHERE version = 39`}
			for v := 34; v <= 38; v++ {
				statements = append(statements, fmt.Sprintf(`INSERT OR IGNORE INTO schema_migrations(version, name, applied_at) VALUES(%d, 'fork %d', 1)`, v, v))
			}
			if !shapePresent {
				statements = append(statements, `ALTER TABLE chats DROP COLUMN read_through_ts`, `ALTER TABLE chats DROP COLUMN read_through_rowid`)
			}
			for _, statement := range statements {
				if _, err := db.sql.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatalf("Open recorded-through-38 store: %v", err)
			}
			defer db.Close()
			if got := chatsSchema(t, db); got != want {
				t.Fatalf("chats schema differs from a fresh store:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
