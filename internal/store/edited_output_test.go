package store

import (
	"testing"
	"time"
)

// The edited columns were written on ingestion but never read back, so callers
// had to open the SQLite file to tell an edited message from an untouched one.
func TestListMessagesReportsEdited(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	ts := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   chat,
		MsgID:     "edited",
		Timestamp: ts,
		Text:      "final text",
		Edited:    true,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !msgs[0].Edited {
		t.Fatal("expected Edited to be true")
	}
	if msgs[0].EditedAt == nil {
		t.Fatal("expected EditedAt to be set")
	}
	if !msgs[0].EditedAt.Equal(ts) {
		t.Fatalf("expected EditedAt %v, got %v", ts, *msgs[0].EditedAt)
	}
}

// An untouched message must not carry an edit timestamp, otherwise every row
// looks edited and the field is useless for filtering.
func TestListMessagesLeavesEditedUnsetForPlainMessage(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   chat,
		MsgID:     "plain",
		Timestamp: time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC),
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Edited {
		t.Fatal("expected Edited to be false")
	}
	if msgs[0].EditedAt != nil {
		t.Fatalf("expected no EditedAt, got %v", *msgs[0].EditedAt)
	}
}

// Search shares the same column list and scanner, so it must report the flag
// too; a message found by search should not look different from the same
// message found by listing it.
func TestSearchMessagesReportsEdited(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   chat,
		MsgID:     "edited",
		Timestamp: time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC),
		Text:      "distinctivetoken",
		Edited:    true,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	msgs, err := db.SearchMessages(SearchMessagesParams{Query: "distinctivetoken", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !msgs[0].Edited {
		t.Fatal("expected Edited to be true from search results")
	}
}
