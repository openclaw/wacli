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
	if ms[0].Snippet != "" {
		t.Fatalf("expected empty snippet for LIKE search, got %q", ms[0].Snippet)
	}
}

// TestSearchLIKEWildcardEscape verifies that LIKE wildcard characters in user
// queries are treated as literals, not SQL pattern chars (#56).
func TestSearchLIKEWildcardEscape(t *testing.T) {
	db := openTestDB(t)
	chat := "555@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Bob", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	msgs := []struct{ id, text string }{
		{"m1", "hello world"},
		{"m2", "100% sure"},
		{"m3", "some_thing here"},
		{"m4", "another message"},
	}
	for _, m := range msgs {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID: chat, MsgID: m.id, Timestamp: time.Now(), Text: m.text,
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", m.id, err)
		}
	}

	t.Run("percent returns only exact match", func(t *testing.T) {
		// Without escaping, '%' would match everything.
		ms, err := db.SearchMessages(SearchMessagesParams{Query: "100%", Limit: 50})
		if err != nil {
			t.Fatalf("SearchMessages: %v", err)
		}
		if len(ms) != 1 || ms[0].MsgID != "m2" {
			t.Fatalf("expected only m2, got %d results: %v", len(ms), ms)
		}
	})

	t.Run("underscore returns only exact match", func(t *testing.T) {
		// Without escaping, '_' would match any single character.
		ms, err := db.SearchMessages(SearchMessagesParams{Query: "some_thing", Limit: 50})
		if err != nil {
			t.Fatalf("SearchMessages: %v", err)
		}
		if len(ms) != 1 || ms[0].MsgID != "m3" {
			t.Fatalf("expected only m3, got %d results: %v", len(ms), ms)
		}
	})
}
