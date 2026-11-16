package store

import (
	"testing"
	"time"
)

func redeliveryParams(chat, id, text string, ts time.Time) UpsertMessageParams {
	return UpsertMessageParams{
		ChatJID:     chat,
		ChatName:    "Alice",
		MsgID:       id,
		SenderJID:   chat,
		SenderName:  "Alice",
		Timestamp:   ts,
		Text:        text,
		DisplayText: text,
	}
}

func openRedeliveryDB(t *testing.T, chat string) *DB {
	t.Helper()
	db := openTestDB(t)
	if err := db.UpsertChat(chat, "dm", "Alice", time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	return db
}

func storedMessageTimestamp(t *testing.T, db *DB, chat, id string) time.Time {
	t.Helper()
	msg, err := db.GetMessage(chat, id)
	if err != nil {
		t.Fatalf("GetMessage %s: %v", id, err)
	}
	return msg.Timestamp
}

func TestUpsertMessageRedeliveryKeepsTheOriginalTimestamp(t *testing.T) {
	chat := "123@s.whatsapp.net"
	db := openRedeliveryDB(t, chat)
	first := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "hello", first)); err != nil {
		t.Fatalf("UpsertMessage first: %v", err)
	}
	for _, later := range []time.Duration{time.Second, 40 * time.Minute, 30 * time.Hour} {
		if err := db.UpsertMessage(redeliveryParams(chat, "m1", "hello", first.Add(later))); err != nil {
			t.Fatalf("UpsertMessage redelivery +%s: %v", later, err)
		}
		if got := storedMessageTimestamp(t, db, chat, "m1"); !got.Equal(first) {
			t.Fatalf("timestamp after a redelivery +%s = %s, want the original %s", later, got, first)
		}
	}
}

func TestUpsertMessageEarlierCopyCorrectsALaterTimestamp(t *testing.T) {
	chat := "123@s.whatsapp.net"
	db := openRedeliveryDB(t, chat)
	original := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	// A redelivery stored first, then the copy that carries the original time.
	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "hello", original.Add(40*time.Minute))); err != nil {
		t.Fatalf("UpsertMessage redelivery: %v", err)
	}
	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "hello", original)); err != nil {
		t.Fatalf("UpsertMessage original: %v", err)
	}
	if got := storedMessageTimestamp(t, db, chat, "m1"); !got.Equal(original) {
		t.Fatalf("timestamp = %s, want the earliest %s", got, original)
	}
}

func TestUpsertMessageRedeliveryKeepsConversationOrder(t *testing.T) {
	chat := "123@s.whatsapp.net"
	db := openRedeliveryDB(t, chat)
	base := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	for i, id := range []string{"m1", "m2", "m3"} {
		if err := db.UpsertMessage(redeliveryParams(chat, id, id, base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("UpsertMessage %s: %v", id, err)
		}
	}
	// WhatsApp delivers m1 again, much later.
	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "m1", base.Add(2*time.Hour))); err != nil {
		t.Fatalf("UpsertMessage redelivery: %v", err)
	}

	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10, Asc: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.MsgID)
	}
	if len(got) != 3 || got[0] != "m1" || got[1] != "m2" || got[2] != "m3" {
		t.Fatalf("order after a redelivery = %v, want [m1 m2 m3]", got)
	}
}

func TestUpsertMessageRedeliveryWithoutTimestampKeepsTheStoredOne(t *testing.T) {
	chat := "123@s.whatsapp.net"
	db := openRedeliveryDB(t, chat)
	first := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "hello", first)); err != nil {
		t.Fatalf("UpsertMessage first: %v", err)
	}
	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "hello", time.Time{})); err != nil {
		t.Fatalf("UpsertMessage without timestamp: %v", err)
	}
	if got := storedMessageTimestamp(t, db, chat, "m1"); !got.Equal(first) {
		t.Fatalf("timestamp = %s, want %s", got, first)
	}
}

func TestUpsertMessageRedeliveryStillCarriesLaterContent(t *testing.T) {
	chat := "123@s.whatsapp.net"
	db := openRedeliveryDB(t, chat)
	first := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)

	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "(message)", first)); err != nil {
		t.Fatalf("UpsertMessage placeholder: %v", err)
	}
	// The retry that finally decrypts arrives later and brings the content.
	if err := db.UpsertMessage(redeliveryParams(chat, "m1", "hello", first.Add(time.Minute))); err != nil {
		t.Fatalf("UpsertMessage decrypted: %v", err)
	}
	msg, err := db.GetMessage(chat, "m1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.Text != "hello" {
		t.Fatalf("text = %q, want the later content", msg.Text)
	}
	if !msg.Timestamp.Equal(first) {
		t.Fatalf("timestamp = %s, want the original %s", msg.Timestamp, first)
	}
}
