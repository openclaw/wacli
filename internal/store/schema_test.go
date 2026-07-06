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
		"quoted_msg_id",
		"quoted_sender_jid",
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

	chatCols, err := tableColumns(db.sql, "chats")
	if err != nil {
		t.Fatalf("chats tableColumns: %v", err)
	}
	if !chatCols["unread_count"] {
		t.Fatalf("expected chats unread_count column to exist")
	}

	statusCols, err := tableColumns(db.sql, "status_messages")
	if err != nil {
		t.Fatalf("status_messages tableColumns: %v", err)
	}
	for _, want := range []string{"msg_id", "ts", "from_me", "sender_jid", "media_key", "background_color", "font"} {
		if !statusCols[want] {
			t.Fatalf("expected status_messages column %q to exist", want)
		}
	}
	if !indexExists(t, db.sql, "idx_status_messages_ts") {
		t.Fatalf("expected status_messages timestamp index to exist")
	}
}

func TestTableHasColumnRejectsUnsafeIdentifier(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.tableHasColumn(`messages); DROP TABLE messages; --`, "msg_id"); err == nil {
		t.Fatalf("expected unsafe table identifier to be rejected")
	}
	if _, err := db.tableHasColumn("messages", `msg_id); DROP TABLE messages; --`); err == nil {
		t.Fatalf("expected unsafe column identifier to be rejected")
	}

	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages"); got != 0 {
		t.Fatalf("messages table was unexpectedly modified, row count = %d", got)
	}
}

func TestTableHasColumnAllowsSchemaIdentifiers(t *testing.T) {
	db := openTestDB(t)

	hasColumn, err := db.tableHasColumn("messages", "display_text")
	if err != nil {
		t.Fatalf("tableHasColumn: %v", err)
	}
	if !hasColumn {
		t.Fatalf("expected messages.display_text to exist")
	}
}

func TestOpenMigratesLegacyUnreadCounts(t *testing.T) {
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
		CREATE TABLE chats (
			jid TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			name TEXT,
			last_message_ts INTEGER,
			archived INTEGER NOT NULL DEFAULT 0,
			pinned INTEGER NOT NULL DEFAULT 0,
			muted_until INTEGER NOT NULL DEFAULT 0,
			unread INTEGER NOT NULL DEFAULT 0
		);
		INSERT INTO chats(jid, kind, unread) VALUES
			('counted@s.whatsapp.net', 'dm', 4),
			('marker@s.whatsapp.net', 'dm', -1),
			('read@s.whatsapp.net', 'dm', 0);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create old schema: %v", err)
	}
	for _, m := range schemaMigrations {
		if m.version >= 18 {
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

	counted, err := db.GetChat("counted@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat counted: %v", err)
	}
	if !counted.Unread || counted.UnreadCount != 4 {
		t.Fatalf("counted unread = %+v, want unread count 4", counted)
	}
	marker, err := db.GetChat("marker@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat marker: %v", err)
	}
	if !marker.Unread || marker.UnreadCount != 0 {
		t.Fatalf("marker unread = %+v, want unread marker only", marker)
	}
	read, err := db.GetChat("read@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat read: %v", err)
	}
	if read.Unread || read.UnreadCount != 0 {
		t.Fatalf("read unread = %+v, want read count 0", read)
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

func TestOpenMigratesStatusMessagesTable(t *testing.T) {
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
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create old schema: %v", err)
	}
	for _, m := range schemaMigrations {
		if m.version >= 17 {
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

	if ok, err := db.tableExists("status_messages"); err != nil || !ok {
		t.Fatalf("status_messages exists=%v err=%v", ok, err)
	}
	if !indexExists(t, db.sql, "idx_status_messages_ts") {
		t.Fatalf("expected status_messages timestamp index to exist")
	}
	if err := db.UpsertStatusMessage(UpsertStatusMessageParams{
		MsgID:     "status-after-upgrade",
		Timestamp: nowUTC(),
		FromMe:    true,
		Text:      "after upgrade",
	}); err != nil {
		t.Fatalf("UpsertStatusMessage after migration: %v", err)
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
