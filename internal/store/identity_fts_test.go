//go:build sqlite_fts5

package store

import (
	"github.com/mattn/go-sqlite3"
	"strings"
	"testing"
	"time"
)

func TestMessageIdentityUpdatesDoNotRewriteFTS(t *testing.T) {
	db := openTestDB(t)
	db.sql.SetMaxOpenConns(1)
	if err := db.UpsertChat("100@g.us", "group", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID: "100@g.us", MsgID: "identity", SenderJID: "123@lid",
		Timestamp: time.Now(), Text: "searchable needle",
	}); err != nil {
		t.Fatal(err)
	}
	// Fault mode: the index cannot accept writes. Identity-only mutations must
	// still succeed, while an indexed-text mutation must fail atomically. This
	// isolates physical index churn that unchanged search results cannot expose.
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		if _, err := db.sql.Exec(`CREATE TRIGGER fixture_fts_` + strings.ToLower(op) + ` BEFORE ` + op + ` ON messages_fts_content BEGIN SELECT RAISE(ABORT,'fixture index write refused');END`); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.sql.Exec(`UPDATE messages SET sender_jid = ?, quoted_sender_jid = ? WHERE msg_id = ?`,
		"15550000001@s.whatsapp.net", "15550000002@s.whatsapp.net", "identity"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE messages SET text='replacement' WHERE msg_id='identity'`); err == nil {
		t.Fatal("indexed mutation bypassed the injected index failure")
	} else if fault, ok := err.(sqlite3.Error); !ok || fault.Code != sqlite3.ErrConstraint {
		t.Fatalf("expected index constraint fault, got %v", err)
	}

	msgs, err := db.SearchMessages(SearchMessagesParams{Query: "needle", Limit: 10})
	if err != nil || len(msgs) != 1 || msgs[0].SenderJID != "15550000001@s.whatsapp.net" {
		t.Fatalf("search after identity update: %+v, %v", msgs, err)
	}
}

func TestSelectiveFTSUpdatesPreserveSearchLifecycle(t *testing.T) {
	db := openTestDB(t)
	if err := db.UpsertChat("100@g.us", "group", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID: "100@g.us", MsgID: "lifecycle", Timestamp: time.Now(), Text: "original",
	}); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"text", "media_caption", "filename", "chat_name", "sender_name", "display_text"} {
		t.Run(column, func(t *testing.T) {
			if _, err := db.sql.Exec(`UPDATE messages SET ` + column + ` = 'replacement'`); err != nil {
				t.Fatal(err)
			}
			msgs, err := db.SearchMessages(SearchMessagesParams{Query: "replacement", Limit: 10})
			if err != nil || len(msgs) != 1 {
				t.Fatalf("search updated %s: %+v, %v", column, msgs, err)
			}
			if _, err := db.sql.Exec(`UPDATE messages SET ` + column + ` = NULL`); err != nil {
				t.Fatal(err)
			}
			msgs, err = db.SearchMessages(SearchMessagesParams{Query: "replacement", Limit: 10})
			if err != nil || len(msgs) != 0 {
				t.Fatalf("search cleared %s: %+v, %v", column, msgs, err)
			}
		})
	}
	for _, step := range []struct {
		query string
		want  int
	}{
		{`UPDATE messages SET text = 'retained'`, 1},
		{`UPDATE messages SET rowid = rowid + 100`, 1},
		{`UPDATE messages SET deleted_at = 1`, 0},
		{`UPDATE messages SET deleted_at = NULL`, 1},
		{`DELETE FROM messages`, 0},
	} {
		if _, err := db.sql.Exec(step.query); err != nil {
			t.Fatal(err)
		}
		msgs, err := db.SearchMessages(SearchMessagesParams{Query: "retained", Limit: 10})
		if err != nil || len(msgs) != step.want {
			t.Fatalf("%s: search = %+v, %v", step.query, msgs, err)
		}
	}
}
