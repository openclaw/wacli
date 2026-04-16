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

func TestHasFTSRemainsEnabledAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open first time: %v", err)
	}
	if !db.HasFTS() {
		t.Fatalf("expected HasFTS=true on first open")
	}
	state, err := db.metadataValue(messagesFTSStateKey)
	if err != nil {
		t.Fatalf("metadataValue on first open: %v", err)
	}
	if state != messagesFTSStateVersion {
		t.Fatalf("expected FTS state marker %q, got %q", messagesFTSStateVersion, state)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close first db: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("Open second time: %v", err)
	}
	defer db.Close()
	if !db.HasFTS() {
		t.Fatalf("expected HasFTS=true after reopen")
	}
	state, err = db.metadataValue(messagesFTSStateKey)
	if err != nil {
		t.Fatalf("metadataValue after reopen: %v", err)
	}
	if state != messagesFTSStateVersion {
		t.Fatalf("expected FTS state marker %q after reopen, got %q", messagesFTSStateVersion, state)
	}
}

func TestEnsureMessagesFTSRepairsMissingTriggersAndBackfillAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open first time: %v", err)
	}

	chat := "repair@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Repair", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    chat,
		ChatName:   "Repair",
		MsgID:      "m1",
		SenderJID:  chat,
		SenderName: "Repair",
		Timestamp:  time.Now(),
		Text:       "first repair message",
	}); err != nil {
		t.Fatalf("UpsertMessage m1: %v", err)
	}

	if _, err := db.sql.Exec(`
		DROP TRIGGER IF EXISTS messages_ai;
		DROP TRIGGER IF EXISTS messages_ad;
		DROP TRIGGER IF EXISTS messages_au;
		DELETE FROM messages_fts;
	`); err != nil {
		t.Fatalf("break FTS state: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close first db: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("Open second time: %v", err)
	}
	defer db.Close()
	if !db.HasFTS() {
		t.Fatalf("expected HasFTS=true after repair")
	}

	ms, err := db.SearchMessages(SearchMessagesParams{Query: "first", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages after repair: %v", err)
	}
	if len(ms) != 1 || ms[0].MsgID != "m1" {
		t.Fatalf("expected rebuilt index to return m1, got %v", ms)
	}

	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    chat,
		ChatName:   "Repair",
		MsgID:      "m2",
		SenderJID:  chat,
		SenderName: "Repair",
		Timestamp:  time.Now().Add(time.Second),
		Text:       "second repair message",
	}); err != nil {
		t.Fatalf("UpsertMessage m2: %v", err)
	}

	ms, err = db.SearchMessages(SearchMessagesParams{Query: "second", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages for new message: %v", err)
	}
	if len(ms) != 1 || ms[0].MsgID != "m2" {
		t.Fatalf("expected recreated triggers to index m2, got %v", ms)
	}
}

func TestEnsureMessagesFTSFallsBackOrRepairsDamagedShadowTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open first time: %v", err)
	}

	chat := "damaged@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Damaged", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    chat,
		ChatName:   "Damaged",
		MsgID:      "m1",
		SenderJID:  chat,
		SenderName: "Damaged",
		Timestamp:  time.Now(),
		Text:       "damaged shadow table message",
	}); err != nil {
		t.Fatalf("UpsertMessage m1: %v", err)
	}

	if _, err := db.sql.Exec(`DROP TABLE messages_fts_data`); err != nil {
		t.Fatalf("damage shadow table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close first db: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("Open after damage should fall back or repair, got: %v", err)
	}
	defer db.Close()

	ms, err := db.SearchMessages(SearchMessagesParams{Query: "damaged", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages after damage: %v", err)
	}
	if len(ms) != 1 || ms[0].MsgID != "m1" {
		t.Fatalf("expected search to keep working after damage, got %v", ms)
	}
}
