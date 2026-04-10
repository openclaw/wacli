//go:build !sqlite_fts5

package store

import (
	"testing"
	"time"
)

func TestEscapeLike(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello", "hello"},
		{"100%", "100\\%"},
		{"_foo", "\\_foo"},
		{"a_b%", "a\\_b\\%"},
		{"back\\slash", "back\\\\slash"},
	}
	for _, tt := range tests {
		got := escapeLike(tt.in)
		if got != tt.want {
			t.Errorf("escapeLike(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSearchLIKEEscapesWildcards(t *testing.T) {
	db := openTestDB(t)

	chat := "wild@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Bob", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	msgs := []struct {
		id, text string
	}{
		{"m1", "100% battery"},
		{"m2", "100x battery"},
		{"m3", "foo_bar"},
		{"m4", "fooxbar"},
	}
	for _, m := range msgs {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID: chat, MsgID: m.id, Text: m.text,
			Timestamp: time.Now(),
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", m.id, err)
		}
	}

	ms, err := db.SearchMessages(SearchMessagesParams{Query: "100%", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(ms) != 1 || ms[0].MsgID != "m1" {
		t.Fatalf("expected exactly m1, got %d results: %+v", len(ms), ms)
	}

	ms, err = db.SearchMessages(SearchMessagesParams{Query: "foo_bar", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(ms) != 1 || ms[0].MsgID != "m3" {
		t.Fatalf("expected exactly m3, got %d results: %+v", len(ms), ms)
	}
}

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
