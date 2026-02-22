//go:build !sqlite_fts5

package store

import (
	"testing"
	"time"
)

func TestSearchMessagesUsesLIKEWhenFTSDisabled(t *testing.T) {
	db := openTestDB(t)
	if db.HasFTS() {
		t.Fatalf("expected HasFTS=false in !sqlite_fts5 build")
	}

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    chat,
		ChatName:   "Alice",
		MsgID:      "m1",
		SenderJID:  chat,
		SenderName: "Alice",
		Timestamp:  time.Now(),
		FromMe:     false,
		Text:       "hello world",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	ms, err := db.SearchMessages(SearchMessagesParams{Query: "hello", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("expected 1 result, got %d", len(ms))
	}
	if ms[0].SenderName != "Alice" {
		t.Fatalf("expected sender name Alice, got %q", ms[0].SenderName)
	}
	if ms[0].Snippet != "" {
		t.Fatalf("expected empty snippet for LIKE search, got %q", ms[0].Snippet)
	}
}
