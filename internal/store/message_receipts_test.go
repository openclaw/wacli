package store

import (
	"testing"
	"time"
)

func seedOutgoing(t *testing.T, db *DB, chat, msgID string, at time.Time) {
	t.Helper()
	if err := db.UpsertChat(chat, "dm", "Alice", at); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID: chat, MsgID: msgID, Timestamp: at, FromMe: true, Text: "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
}

func TestMessageReceiptsOnlyEverMoveForward(t *testing.T) {
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, db, chat, "m1", at)

	if err := db.UpsertMessageReceipt(chat, "m1", chat, "delivered", at.Add(time.Minute)); err != nil {
		t.Fatalf("delivered: %v", err)
	}
	counts, err := db.MessageReceipts(chat, "m1")
	if err != nil {
		t.Fatalf("MessageReceipts: %v", err)
	}
	if counts.Delivered != 1 || counts.Read != 0 {
		t.Fatalf("after delivery = %+v, want one delivery and no read", counts)
	}

	if err := db.UpsertMessageReceipt(chat, "m1", chat, "read", at.Add(2*time.Minute)); err != nil {
		t.Fatalf("read: %v", err)
	}
	counts, _ = db.MessageReceipts(chat, "m1")
	if counts.Delivered != 1 || counts.Read != 1 {
		t.Fatalf("after read = %+v, want the same recipient counted once, as read", counts)
	}

	// A late delivery receipt must not take the message back to gray ticks.
	if err := db.UpsertMessageReceipt(chat, "m1", chat, "delivered", at.Add(3*time.Minute)); err != nil {
		t.Fatalf("late delivery: %v", err)
	}
	counts, _ = db.MessageReceipts(chat, "m1")
	if counts.Read != 1 {
		t.Fatalf("after a late delivery = %+v, want the read kept", counts)
	}

	if err := db.UpsertMessageReceipt(chat, "m1", chat, "typing", at); err == nil {
		t.Fatal("an unknown status was accepted")
	}
	if err := db.UpsertMessageReceipt(chat, "m1", "  ", "read", at); err == nil {
		t.Fatal("an empty recipient was accepted")
	}
}

func TestReceiptsForMessagesWeDoNotHaveAreDropped(t *testing.T) {
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, db, chat, "m1", at)

	// A message this store never saw, in a chat it never saw: neither is worth
	// a row, and neither is an error.
	if err := db.UpsertMessageReceipt(chat, "unknown-message", chat, "read", at); err != nil {
		t.Fatalf("unknown message: %v", err)
	}
	if err := db.UpsertMessageReceipt("999@s.whatsapp.net", "m1", chat, "read", at); err != nil {
		t.Fatalf("unknown chat: %v", err)
	}
	// A message they sent us has no ticks of ours to report on.
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID: chat, MsgID: "theirs", Timestamp: at, Text: "hi",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	if err := db.UpsertMessageReceipt(chat, "theirs", chat, "read", at); err != nil {
		t.Fatalf("incoming message: %v", err)
	}
	for _, c := range [][2]string{{chat, "unknown-message"}, {"999@s.whatsapp.net", "m1"}, {chat, "theirs"}} {
		counts, err := db.MessageReceipts(c[0], c[1])
		if err != nil {
			t.Fatalf("MessageReceipts: %v", err)
		}
		if counts.Delivered != 0 {
			t.Fatalf("%v carries %+v, want nothing stored", c, counts)
		}
	}
}

func TestMessageListCarriesReceiptCounts(t *testing.T) {
	db := openTestDB(t)
	group := "999@g.us"
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, db, group, "g1", at)

	for _, r := range []struct{ jid, status string }{
		{"111@s.whatsapp.net", "delivered"},
		{"222@s.whatsapp.net", "read"},
		{"333@s.whatsapp.net", "played"},
	} {
		if err := db.UpsertMessageReceipt(group, "g1", r.jid, r.status, at.Add(time.Minute)); err != nil {
			t.Fatalf("receipt %s: %v", r.jid, err)
		}
	}

	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: group, Limit: 5})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	// Three recipients have it, two of them have read it: enough to tell a group
	// where everybody has read from one where somebody has.
	if msgs[0].DeliveredTo != 3 || msgs[0].ReadBy != 2 {
		t.Fatalf("counts = %d delivered, %d read; want 3 and 2", msgs[0].DeliveredTo, msgs[0].ReadBy)
	}

	other, err := db.ListMessages(ListMessagesParams{ChatJID: "123@s.whatsapp.net", Limit: 5})
	if err != nil {
		t.Fatalf("ListMessages other chat: %v", err)
	}
	for _, m := range other {
		if m.DeliveredTo != 0 || m.ReadBy != 0 {
			t.Fatalf("a message nobody reported on carries %+v", m)
		}
	}
}
