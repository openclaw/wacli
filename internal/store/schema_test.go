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
		"is_forwarded",
		"forwarding_score",
		"reaction_to_id",
		"reaction_emoji",
		"local_path",
		"downloaded_at",
		"revoked",
		"deleted_for_me",
	} {
		if !cols[want] {
			t.Fatalf("expected messages column %q to exist", want)
		}
	}

	groupCols, err := tableColumns(db.sql, "groups")
	if err != nil {
		t.Fatalf("groups tableColumns: %v", err)
	}
	for _, want := range []string{"is_parent", "linked_parent_jid"} {
		if !groupCols[want] {
			t.Fatalf("expected groups column %q to exist", want)
		}
	}

	starredCols, err := tableColumns(db.sql, "starred")
	if err != nil {
		t.Fatalf("starred tableColumns: %v", err)
	}
	for _, want := range []string{"chat_jid", "msg_id", "sender_jid", "from_me", "starred_at"} {
		if !starredCols[want] {
			t.Fatalf("expected starred column %q to exist", want)
		}
	}
}

func TestOpenMigratesGroupCommunityHierarchyColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE groups (
			jid TEXT PRIMARY KEY,
			name TEXT,
			owner_jid TEXT,
			created_ts INTEGER,
			left_at INTEGER,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
		INSERT INTO schema_migrations(version, name, applied_at) VALUES
			(1, 'core schema', 1),
			(2, 'messages display_text column', 1),
			(3, 'messages fts', 1),
			(4, 'groups left_at column', 1),
			(5, 'messages forwarded columns', 1),
			(6, 'messages reaction columns', 1),
			(7, 'starred messages', 1),
			(8, 'messages revoked column', 1),
			(9, 'messages deleted_for_me column', 1),
			(10, 'chat state columns', 1);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create old schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw DB: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	cols, err := tableColumns(db.sql, "groups")
	if err != nil {
		t.Fatalf("groups tableColumns: %v", err)
	}
	for _, want := range []string{"is_parent", "linked_parent_jid"} {
		if !cols[want] {
			t.Fatalf("expected migrated groups column %q to exist", want)
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
