//go:build sqlite_fts5

package store

import (
	"testing"
	"time"
)

func TestSanitizeFTSQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", `"hello" "world"`},
		{"hello OR world", `"hello" "OR" "world"`},
		{"NOT secret", `"NOT" "secret"`},
		{`say "hi"`, `"say" """hi"""`},
		{"col:value", `"col:value"`},
		{"test*", `"test*"`},
		{"NEAR(a b)", `"NEAR(a" "b)"`},
		{"  spaced  ", `"spaced"`},
	}
	for _, tt := range tests {
		got := sanitizeFTSQuery(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFTSSearchWithSpecialCharacters(t *testing.T) {
	db := openTestDB(t)
	if !db.HasFTS() {
		t.Fatalf("expected HasFTS=true in sqlite_fts5 build")
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
		Text:       "meeting notes for project",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	// These queries previously caused FTS5 parse errors or unexpected behavior.
	dangerous := []string{
		`"unclosed`,
		`hello OR world`,
		`NOT meeting`,
		`col:meeting`,
		`test*`,
		`NEAR(meeting notes)`,
		`meeting AND notes`,
	}
	for _, q := range dangerous {
		_, err := db.SearchMessages(SearchMessagesParams{Query: q, Limit: 10})
		if err != nil {
			t.Errorf("SearchMessages(%q) failed: %v", q, err)
		}
	}

	// Verify a normal search still finds the message.
	ms, err := db.SearchMessages(SearchMessagesParams{Query: "meeting", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("expected 1 result for 'meeting', got %d", len(ms))
	}
}

func TestSearchMessagesUsesFTSWhenEnabled(t *testing.T) {
	db := openTestDB(t)
	if !db.HasFTS() {
		t.Fatalf("expected HasFTS=true in sqlite_fts5 build")
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
	if ms[0].Snippet == "" {
		t.Fatalf("expected snippet for FTS search, got empty")
	}
}
