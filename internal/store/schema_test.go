package store

import (
	"database/sql"
	"errors"
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
		"edited",
		"edited_ts",
	} {
		if !cols[want] {
			t.Fatalf("expected messages column %q to exist", want)
		}
	}

	callCols, err := tableColumns(db.sql, "call_events")
	if err != nil {
		t.Fatalf("call_events tableColumns: %v", err)
	}
	for _, want := range []string{"chat_jid", "call_id", "event_type", "direction", "media", "outcome", "duration_secs", "participants"} {
		if !callCols[want] {
			t.Fatalf("expected call_events column %q to exist", want)
		}
	}
	if !indexExists(t, db.sql, "idx_call_events_chat_ts") {
		t.Fatalf("expected call_events chat index to exist")
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

	groupCols, err := tableColumns(db.sql, "groups")
	if err != nil {
		t.Fatalf("groups tableColumns: %v", err)
	}
	for _, want := range []string{"is_parent", "linked_parent_jid"} {
		if !groupCols[want] {
			t.Fatalf("expected groups column %q to exist", want)
		}
	}
	if !indexExists(t, db.sql, "idx_groups_linked_parent_jid") {
		t.Fatalf("expected linked-parent group index to exist")
	}

	contactCols, err := tableColumns(db.sql, "contacts")
	if err != nil {
		t.Fatalf("contacts tableColumns: %v", err)
	}
	if !contactCols["system_name"] {
		t.Fatalf("expected contacts system_name column to exist")
	}
}

func TestOpenRepairsRecordedCallEventsMigrationMissingTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`
		DROP TABLE call_events;
		INSERT OR IGNORE INTO schema_migrations(version, name, applied_at) VALUES(14, 'call events', 1);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create inconsistent schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("Open repaired DB: %v", err)
	}
	defer db.Close()

	if ok, err := db.tableExists("call_events"); err != nil || !ok {
		t.Fatalf("call_events exists=%v err=%v", ok, err)
	}
	if !indexExists(t, db.sql, "idx_call_events_chat_ts") {
		t.Fatalf("expected call_events chat index to be recreated")
	}
	if _, err := db.ListCallEvents(ListCallEventsParams{Limit: 1}); err != nil {
		t.Fatalf("ListCallEvents after schema repair: %v", err)
	}
}

func TestOpenMigratesGroupHierarchyColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
		CREATE TABLE groups (
			jid TEXT PRIMARY KEY,
			name TEXT,
			owner_jid TEXT,
			created_ts INTEGER,
			left_at INTEGER,
			updated_at INTEGER NOT NULL
		);
		INSERT INTO groups(jid, name, updated_at) VALUES('g@g.us', 'Old', 1);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create old schema: %v", err)
	}
	for _, m := range schemaMigrations {
		if m.version >= 11 {
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, m.version, m.name); err != nil {
			_ = raw.Close()
			t.Fatalf("mark migration %d: %v", m.version, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated DB: %v", err)
	}
	defer db.Close()

	groupCols, err := tableColumns(db.sql, "groups")
	if err != nil {
		t.Fatalf("groups tableColumns: %v", err)
	}
	for _, want := range []string{"is_parent", "linked_parent_jid"} {
		if !groupCols[want] {
			t.Fatalf("expected migrated groups column %q to exist", want)
		}
	}
	if !indexExists(t, db.sql, "idx_groups_linked_parent_jid") {
		t.Fatalf("expected migrated linked-parent group index to exist")
	}
}

func TestOpenMigratesLegacyGroupsWithoutMigrationTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

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
		INSERT INTO groups(jid, name, updated_at) VALUES('g@g.us', 'Old', 1);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy DB: %v", err)
	}
	defer db.Close()

	groupCols, err := tableColumns(db.sql, "groups")
	if err != nil {
		t.Fatalf("groups tableColumns: %v", err)
	}
	for _, want := range []string{"is_parent", "linked_parent_jid"} {
		if !groupCols[want] {
			t.Fatalf("expected migrated groups column %q to exist", want)
		}
	}
	if !indexExists(t, db.sql, "idx_groups_linked_parent_jid") {
		t.Fatalf("expected migrated linked-parent group index to exist")
	}
}

func TestOpenMigratesContactsSystemNameColumn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")

	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
		CREATE TABLE contacts (
			jid TEXT PRIMARY KEY,
			phone TEXT,
			push_name TEXT,
			full_name TEXT,
			first_name TEXT,
			business_name TEXT,
			updated_at INTEGER NOT NULL
		);
		INSERT INTO contacts(jid, phone, updated_at) VALUES('111@s.whatsapp.net', '111', 1);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create old contacts schema: %v", err)
	}
	for _, m := range schemaMigrations {
		if m.version >= 12 {
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, m.version, m.name); err != nil {
			_ = raw.Close()
			t.Fatalf("mark migration %d: %v", m.version, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated DB: %v", err)
	}
	defer db.Close()

	contactCols, err := tableColumns(db.sql, "contacts")
	if err != nil {
		t.Fatalf("contacts tableColumns: %v", err)
	}
	if !contactCols["system_name"] {
		t.Fatalf("expected migrated contacts system_name column")
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

func indexExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query index %q: %v", name, err)
	}
	return found == name
}
