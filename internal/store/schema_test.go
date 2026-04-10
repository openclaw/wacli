package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesExpectedSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	cols, err := tableColumns(db.sql, "messages")
	if err != nil {
		t.Fatalf("tableColumns: %v", err)
	}

	for _, want := range []string{
		"chat_name",
		"sender_name",
		"display_text",
		"local_path",
		"downloaded_at",
	} {
		if !cols[want] {
			t.Fatalf("expected messages column %q to exist", want)
		}
	}

	backfillCols, err := tableColumns(db.sql, "history_backfill_state")
	if err != nil {
		t.Fatalf("tableColumns history_backfill_state: %v", err)
	}
	for _, want := range []string{
		"chat_jid",
		"status",
		"last_backfill_at",
		"last_success_at",
		"requests_sent_total",
		"responses_seen_total",
		"consecutive_noop_requests",
		"reached_start",
		"blocked_reason",
		"last_error",
		"updated_at",
	} {
		if !backfillCols[want] {
			t.Fatalf("expected history_backfill_state column %q to exist", want)
		}
	}
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[strings.ToLower(name)] = true
	}
	return cols, rows.Err()
}
