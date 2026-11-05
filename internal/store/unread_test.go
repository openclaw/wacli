package store

import (
	"testing"
	"time"
)

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

func TestSemanticUnreadContentPredicates(t *testing.T) {
	for _, kind := range []string{"poll", "poll_vote", "poll_update", "contact", "contacts", "event"} {
		t.Run(kind, func(t *testing.T) {
			db := openTestDB(t)
			chat := "123@s.whatsapp.net"
			at := time.Date(2024, 1, 3, 10, 0, 0, 0, time.UTC)
			if err := db.UpsertChat(chat, "dm", "Chat", at); err != nil {
				t.Fatal(err)
			}
			for _, p := range []UpsertMessageParams{
				{ChatJID: chat, MsgID: "text", Timestamp: at, Text: "hello"},
				{ChatJID: chat, MsgID: kind, Timestamp: at.Add(time.Minute), Kind: kind, DisplayText: "(message)"},
			} {
				if err := db.UpsertMessage(p); err != nil {
					t.Fatal(err)
				}
			}
			for _, markerOnly := range []bool{false, true} {
				count := 2
				if markerOnly {
					count = 0
				}
				if err := db.SetChatUnreadCount(chat, count); err != nil {
					t.Fatal(err)
				}
				if err := db.SetChatUnread(chat, true); err != nil {
					t.Fatal(err)
				}
				clear := db.ClearChatUnreadThrough
				if markerOnly {
					clear = db.ClearChatUnreadThroughPreservingMarker
				}
				if err := clear(chat, at, []string{"text"}); err != nil {
					t.Fatal(err)
				}
				c, err := db.GetChat(chat)
				if err != nil || !c.Unread || (!markerOnly && c.UnreadCount != 1) {
					t.Errorf("markerOnly=%v: remaining semantic content lost: %+v, err %v", markerOnly, c, err)
				}
			}
			if err := db.SetChatUnreadCount(chat, 1); err != nil {
				t.Fatal(err)
			}
			msgs, err := db.ReadReceiptCandidatesThrough(chat, kind, 100)
			if err != nil || len(msgs) != 1 || msgs[0].MsgID != kind {
				t.Errorf("semantic receipt selection = %+v, err %v", msgs, err)
			}
			// An empty-selection race must not erase a semantic arrival.
			if err := db.MarkMessageDeletedForMe(chat, "text", "", false, at.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := db.ClearChatUnreadMarker(chat); err != nil {
				t.Fatal(err)
			}
			c, err := db.GetChat(chat)
			if err != nil || !c.Unread || c.UnreadCount != 1 {
				t.Errorf("marker clear lost semantic unread: %+v, err %v", c, err)
			}
		})
	}
}

func TestReadReceiptContentOnLegacyReadOnlyStore(t *testing.T) {
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	at := time.Unix(1700000000, 0)
	if err := db.UpsertChat(chat, "dm", "Chat", at); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "text", Timestamp: at, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatUnreadCount(chat, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TRIGGER retrieval_messages_update; ALTER TABLE messages DROP COLUMN kind`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.ReadBoundary(chat, "text"); err != nil {
		t.Fatal(err)
	}
	msgs, err := ro.ReadReceiptCandidatesThrough(chat, "text", 100)
	if err != nil || len(msgs) != 1 || msgs[0].MsgID != "text" {
		t.Fatalf("legacy receipt selection = %+v, err %v", msgs, err)
	}
}
