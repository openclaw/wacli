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
