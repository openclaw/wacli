package store

import (
	"testing"
	"time"
)

const ownReadChat = "5551230000@s.whatsapp.net"

var ownReadBase = time.Unix(1700000000, 0).UTC()

func seedUnreadChat(t *testing.T, db *DB, jid string, count int, marker bool) {
	t.Helper()
	if err := db.UpsertChat(jid, "dm", "Alice", ownReadBase); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if count > 0 {
		if err := db.SetChatUnreadCount(jid, count); err != nil {
			t.Fatalf("SetChatUnreadCount: %v", err)
		}
	} else if marker {
		if err := db.SetChatUnread(jid, true); err != nil {
			t.Fatalf("SetChatUnread: %v", err)
		}
	}
}

func storeMsg(t *testing.T, db *DB, p UpsertMessageParams) {
	t.Helper()
	if p.ChatJID == "" {
		p.ChatJID = ownReadChat
	}
	if err := db.UpsertMessage(p); err != nil {
		t.Fatalf("UpsertMessage %s: %v", p.MsgID, err)
	}
}

func assertUnread(t *testing.T, db *DB, jid string, unread bool, count int) {
	t.Helper()
	c, err := db.GetChat(jid)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Unread != unread || c.UnreadCount != count {
		t.Fatalf("unread state = (%v, %d), want (%v, %d)", c.Unread, c.UnreadCount, unread, count)
	}
}

func TestOwnMessageClearsUnreadCountAndMarker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chat   string
		count  int
		marker bool
	}{
		{"direct count", ownReadChat, 3, false},
		{"direct manual marker", ownReadChat, 0, true},
		{"group count", "120363000000000000@g.us", 5, false},
		{"group manual marker", "120363000000000000@g.us", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedUnreadChat(t, db, tc.chat, tc.count, tc.marker)
			storeMsg(t, db, UpsertMessageParams{ChatJID: tc.chat, MsgID: "in-1", Timestamp: ownReadBase.Add(time.Minute), Text: "hi"})
			storeMsg(t, db, UpsertMessageParams{ChatJID: tc.chat, MsgID: "own-1", Timestamp: ownReadBase.Add(2 * time.Minute), FromMe: true, Text: "on it"})
			assertUnread(t, db, tc.chat, false, 0)
		})
	}
}

func TestOwnMediaMessageClearsUnread(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 2, false)
	storeMsg(t, db, UpsertMessageParams{MsgID: "own-img", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, MediaType: "image", DisplayText: "Sent image"})
	assertUnread(t, db, ownReadChat, false, 0)
}

func TestOwnMessageNotNewerThanIncomingKeepsUnread(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 2, false)
	storeMsg(t, db, UpsertMessageParams{MsgID: "in-late", Timestamp: ownReadBase.Add(10 * time.Minute), Text: "newer"})
	// A historical own message older than a received one must not swallow it.
	storeMsg(t, db, UpsertMessageParams{MsgID: "own-old", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "old reply"})
	assertUnread(t, db, ownReadChat, true, 2)

	seedUnreadChat(t, db, "5559990000@s.whatsapp.net", 0, true)
	storeMsg(t, db, UpsertMessageParams{ChatJID: "5559990000@s.whatsapp.net", MsgID: "in", Timestamp: ownReadBase.Add(10 * time.Minute), Text: "newer"})
	storeMsg(t, db, UpsertMessageParams{ChatJID: "5559990000@s.whatsapp.net", MsgID: "own-old", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "old reply"})
	assertUnread(t, db, "5559990000@s.whatsapp.net", true, 0)
}

func TestOwnMessageIgnoresNonReadableIncomingWhenComparing(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 1, false)
	// A later reaction, protocol row or revoked message is not unread content.
	storeMsg(t, db, UpsertMessageParams{MsgID: "in-react", Timestamp: ownReadBase.Add(9 * time.Minute), ReactionToID: "x", ReactionEmoji: "+1"})
	storeMsg(t, db, UpsertMessageParams{MsgID: "in-proto", Timestamp: ownReadBase.Add(9 * time.Minute), DisplayText: "(message)"})
	storeMsg(t, db, UpsertMessageParams{MsgID: "in-revoked", Timestamp: ownReadBase.Add(9 * time.Minute), Revoked: true})
	storeMsg(t, db, UpsertMessageParams{MsgID: "own", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "ok"})
	assertUnread(t, db, ownReadChat, false, 0)
}

func TestOwnNonReplyRowsKeepUnread(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    UpsertMessageParams
	}{
		{"reaction", UpsertMessageParams{ReactionToID: "in-1", ReactionEmoji: "+1", DisplayText: "Reacted + to message"}},
		{"reaction removal", UpsertMessageParams{ReactionToID: "in-1"}},
		{"edit", UpsertMessageParams{Edited: true, Text: "fixed"}},
		{"revoke", UpsertMessageParams{Revoked: true}},
		{"delete for me", UpsertMessageParams{DeletedForMe: true}},
		{"protocol placeholder", UpsertMessageParams{DisplayText: "(message)"}},
		{"explicit skip", UpsertMessageParams{Text: "call", SkipOwnReadMark: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedUnreadChat(t, db, ownReadChat, 1, false)
			storeMsg(t, db, UpsertMessageParams{MsgID: "in-1", Timestamp: ownReadBase.Add(time.Minute), Text: "hi"})
			tc.p.MsgID = "own-x"
			tc.p.FromMe = true
			tc.p.Timestamp = ownReadBase.Add(5 * time.Minute)
			storeMsg(t, db, tc.p)
			assertUnread(t, db, ownReadChat, true, 1)
		})
	}
}

func TestOwnMessageMarksReadIsIdempotentAndReplaySafe(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 1, false)
	own := UpsertMessageParams{MsgID: "own-1", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "on it"}
	storeMsg(t, db, own)
	storeMsg(t, db, own)
	assertUnread(t, db, ownReadChat, false, 0)

	// The user marks the chat unread by hand afterwards; replaying the same
	// reply (retry, backfill) must not undo it.
	if err := db.SetChatUnread(ownReadChat, true); err != nil {
		t.Fatal(err)
	}
	storeMsg(t, db, own)
	assertUnread(t, db, ownReadChat, true, 0)

	// A different own message is a new reply and clears it again.
	storeMsg(t, db, UpsertMessageParams{MsgID: "own-2", Timestamp: ownReadBase.Add(2 * time.Minute), FromMe: true, Text: "done"})
	assertUnread(t, db, ownReadChat, false, 0)
}

func TestOwnPlaceholderLaterDecodedCountsOnce(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 1, false)
	storeMsg(t, db, UpsertMessageParams{MsgID: "own-1", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, DisplayText: "(message)"})
	assertUnread(t, db, ownReadChat, true, 1)
	storeMsg(t, db, UpsertMessageParams{MsgID: "own-1", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "decoded"})
	assertUnread(t, db, ownReadChat, false, 0)
}

func TestClearChatUnreadByOwnMessageNeverCreatesChats(t *testing.T) {
	db := openTestDB(t)
	if err := db.ClearChatUnreadByOwnMessage(ownReadChat, ownReadBase); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetChat(ownReadChat); err == nil {
		t.Fatal("clearing unread created a chat")
	}
}
