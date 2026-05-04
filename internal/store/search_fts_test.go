//go:build sqlite_fts5

package store

import (
	"path/filepath"
	"testing"
	"time"
)

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

func TestExistingEmptyFTSTableDetectedOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !db.HasFTS() {
		t.Fatalf("expected initial FTS migration to enable FTS")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if !db.HasFTS() {
		t.Fatalf("expected existing empty FTS table to enable FTS after reopen")
	}
}

func TestMigrateLIDToPNMaintainsFTSRows(t *testing.T) {
	db := openTestDB(t)
	if !db.HasFTS() {
		t.Skip("FTS5 not enabled")
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	if err := db.UpsertChat(lid, "unknown", lid, base); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   lid,
		MsgID:     "m-lid",
		SenderJID: lid,
		Timestamp: base,
		Text:      "needle migrated",
	}); err != nil {
		t.Fatalf("UpsertMessage lid: %v", err)
	}

	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}

	msgs, err := db.SearchMessages(SearchMessagesParams{Query: "needle", ChatJID: pn, Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("search results = %d, want 1", len(msgs))
	}
	if msgs[0].ChatJID != pn {
		t.Fatalf("search result chat = %q, want %q", msgs[0].ChatJID, pn)
	}
}

// TestSanitizeFTSQuery verifies that user input is sanitized before being
// passed to the FTS5 MATCH clause, preventing query-syntax injection (#57).
func TestSanitizeFTSQuery(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Basic tokens are individually quoted.
		{"hello", `"hello"`},
		{"hello world", `"hello" "world"`},
		// FTS5 operators are neutralised — treated as literal tokens.
		{"hello OR world", `"hello" "OR" "world"`},
		{"NOT secret", `"NOT" "secret"`},
		{"hello AND world", `"hello" "AND" "world"`},
		// Column filter syntax is neutralised.
		{"col:value", `"col:value"`},
		// Prefix wildcard is neutralised.
		{"test*", `"test*"`},
		// NEAR operator is neutralised.
		{"NEAR(a b)", `"NEAR(a" "b)"`},
		// Embedded double-quotes are escaped by doubling.
		{`say "hi"`, `"say" """hi"""`},
		// Extra whitespace is collapsed.
		{"  spaced  ", `"spaced"`},
		// Empty / blank input returns empty quoted token.
		{"", `""`},
		{"   ", `""`},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := sanitizeFTSQuery(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestFTSInjectionPrevented verifies end-to-end that FTS5 syntax in user
// queries does not cause errors or unexpected results (#57).
func TestFTSInjectionPrevented(t *testing.T) {
	db := openTestDB(t)
	if !db.HasFTS() {
		t.Skip("FTS5 not enabled")
	}

	chat := "555@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Bob", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	msgs := []struct{ id, text string }{
		{"m1", "hello world"},
		{"m2", "price is 100% confirmed"},
		{"m3", "OR operator test"},
	}
	for _, m := range msgs {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID: chat, MsgID: m.id, Timestamp: time.Now(), Text: m.text,
		}); err != nil {
			t.Fatalf("UpsertMessage: %v", err)
		}
	}

	injectionQueries := []string{
		"OR hello",          // bare OR would be a syntax error in raw FTS5
		"NOT hello",         // bare NOT would be a syntax error
		"hello AND world",   // AND as operator vs literal
		"NEAR(hello world)", // NEAR function syntax
		`"hello"`,           // raw quoted phrase
	}

	for _, q := range injectionQueries {
		t.Run(q, func(t *testing.T) {
			// Must not panic or return an error — injection is neutralised.
			_, err := db.SearchMessages(SearchMessagesParams{Query: q, Limit: 10})
			if err != nil {
				t.Errorf("SearchMessages(%q) returned unexpected error: %v", q, err)
			}
		})
	}

	// Multi-word search should still work (implicit AND between tokens).
	t.Run("multi-word implicit AND", func(t *testing.T) {
		ms, err := db.SearchMessages(SearchMessagesParams{Query: "hello world", Limit: 10})
		if err != nil {
			t.Fatalf("SearchMessages: %v", err)
		}
		if len(ms) != 1 || ms[0].MsgID != "m1" {
			t.Errorf("expected m1 for 'hello world', got %v", ms)
		}
	})
}
