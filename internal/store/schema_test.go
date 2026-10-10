package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		"media_unavailable_at",
		"revoked",
		"deleted_for_me",
		"deleted_at",
		"deletion_reason",
		"payload_purged_at",
		"edited",
		"edited_ts",
		"buttons",
		"ad_referral",
	} {
		if !cols[want] {
			t.Fatalf("expected messages column %q to exist", want)
		}
	}
	if exists, err := db.tableExists("message_payload_purges"); err != nil || !exists {
		t.Fatalf("message_payload_purges table exists = %v, err = %v", exists, err)
	}
	if exists, err := db.tableExists("message_local_media_aliases"); err != nil || !exists {
		t.Fatalf("message_local_media_aliases table exists = %v, err = %v", exists, err)
	}

	locationCols, err := tableColumns(db.sql, "message_locations")
	if err != nil {
		t.Fatalf("message_locations tableColumns: %v", err)
	}
	for _, want := range []string{"chat_jid", "msg_id", "latitude", "longitude", "name", "address", "is_live"} {
		if !locationCols[want] {
			t.Fatalf("expected message_locations column %q to exist", want)
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
	if exists, err := db.tableExists("app_state_recovery_required"); err != nil {
		t.Fatalf("app_state_recovery_required tableExists: %v", err)
	} else if exists {
		t.Fatal("legacy app_state_recovery_required table still exists")
	}
	if exists, err := db.tableExists("app_state_recovery_intents"); err != nil {
		t.Fatalf("app_state_recovery_intents tableExists: %v", err)
	} else if !exists {
		t.Fatal("expected app_state_recovery_intents table to exist")
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

func TestOpenMigratesLegacyMessageTombstonesWithoutPayloadLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := strings.Replace(coreSchemaSQL, "    deleted_at INTEGER,\n    deletion_reason TEXT,\n    payload_purged_at INTEGER,\n", "", 1)
	legacySchema = strings.Replace(legacySchema, `CREATE TABLE IF NOT EXISTS message_payload_purges (
    chat_jid TEXT NOT NULL,
    msg_id TEXT NOT NULL,
    purged_at INTEGER NOT NULL,
    deleted_at INTEGER NOT NULL,
    deletion_reason TEXT NOT NULL,
    PRIMARY KEY (chat_jid, msg_id)
);

`, "", 1)
	if _, err := raw.Exec(legacySchema + `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
		INSERT INTO chats(jid, kind, name) VALUES('chat@s.whatsapp.net', 'dm', 'Alice');
		INSERT INTO messages(chat_jid, msg_id, ts, from_me, text, display_text, media_type, filename, revoked, buttons)
		VALUES('chat@s.whatsapp.net', 'mid', 123, 1, 'retained text', 'retained display', 'document', 'proof.pdf', 1, '[{"type":"url","display_text":"Open","url":"https://example.com"}]');
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy store: %v", err)
	}
	for _, migration := range schemaMigrations {
		if migration.version >= 21 {
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, migration.version, migration.name); err != nil {
			_ = raw.Close()
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated store: %v", err)
	}
	defer db.Close()
	msg, err := db.GetMessage("chat@s.whatsapp.net", "mid")
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text != "retained text" || msg.DisplayText != "retained display" || msg.MediaType != "document" || msg.Filename != "proof.pdf" || len(msg.Buttons) != 1 {
		t.Fatalf("migrated payload = %+v", msg)
	}
	if msg.DeletedAt == nil || msg.DeletedAt.Unix() != 123 || msg.DeletionReason != "legacy-whatsapp-revoke" {
		t.Fatalf("migrated tombstone = %v %q", msg.DeletedAt, msg.DeletionReason)
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

func TestOpenRepairsRecordedMediaUnavailableMigrationMissingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	legacySchema := strings.Replace(coreSchemaSQL, "    media_unavailable_at INTEGER,\n", "", 1)
	if _, err := raw.Exec(legacySchema + `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	for _, migration := range schemaMigrations {
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, migration.version, migration.name); err != nil {
			_ = raw.Close()
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open repaired DB: %v", err)
	}
	defer db.Close()
	hasColumn, err := db.tableHasColumn("messages", "media_unavailable_at")
	if err != nil {
		t.Fatalf("tableHasColumn: %v", err)
	}
	if !hasColumn {
		t.Fatalf("expected media_unavailable_at repair")
	}
}

func TestOpenAddsAdReferralColumnToLegacyMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	legacySchema := strings.Replace(coreSchemaSQL, "    ad_referral TEXT,\n", "", 1)
	if _, err := raw.Exec(legacySchema + `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	for _, migration := range schemaMigrations {
		if migration.version >= 30 {
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, migration.version, migration.name); err != nil {
			_ = raw.Close()
			t.Fatalf("record migration %d: %v", migration.version, err)
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
	hasColumn, err := db.tableHasColumn("messages", "ad_referral")
	if err != nil {
		t.Fatalf("tableHasColumn: %v", err)
	}
	if !hasColumn {
		t.Fatalf("expected messages.ad_referral after migration")
	}
}

// buildPopulatedV0200Store writes a store at the exact schema level that
// shipped in v0.20.0 (testdata/schema_v0.20.0.sql plus migration records
// 1..28) and populates representative rows: a plain message, a starred
// message with buttons, a location, a hidden-identity (LID) alias row, and a
// tombstoned message with its purge-ledger record.
func buildPopulatedV0200Store(t *testing.T, path string) {
	t.Helper()
	legacySchema, err := os.ReadFile(filepath.Join("testdata", "schema_v0.20.0.sql"))
	if err != nil {
		t.Fatalf("read v0.20.0 schema fixture: %v", err)
	}
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(string(legacySchema) + `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create v0.20.0 schema: %v", err)
	}
	for _, migration := range schemaMigrations {
		if migration.version > 28 { // 28 is the last migration shipped in v0.20.0
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, migration.version, migration.name); err != nil {
			_ = raw.Close()
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO chats(jid, kind, name, last_message_ts) VALUES('15551234567@s.whatsapp.net', 'dm', 'Alice', 1700000300)`, nil},
		{`INSERT INTO chats(jid, kind, name, last_message_ts) VALUES('999123456789@lid', 'dm', 'Hidden Alice', 1700000400)`, nil},
		{`INSERT INTO messages(chat_jid, msg_id, sender_jid, ts, from_me, text) VALUES('15551234567@s.whatsapp.net', 'plain1', '15551234567@s.whatsapp.net', 1700000100, 0, 'hello from v0.20.0')`, nil},
		{`INSERT INTO messages(chat_jid, msg_id, sender_jid, ts, from_me, text, buttons) VALUES('15551234567@s.whatsapp.net', 'tmpl1', '15551234567@s.whatsapp.net', 1700000200, 0, 'Check our deals', ?)`, []any{`[{"type":"url","display_text":"Buy flights","url":"https://example.com/flights"}]`}},
		{`INSERT INTO starred(chat_jid, msg_id, starred_at) VALUES('15551234567@s.whatsapp.net', 'tmpl1', 1700000500)`, nil},
		{`INSERT INTO messages(chat_jid, msg_id, sender_jid, ts, from_me, text) VALUES('15551234567@s.whatsapp.net', 'loc1', '15551234567@s.whatsapp.net', 1700000250, 0, 'see you here')`, nil},
		{`INSERT INTO message_locations(chat_jid, msg_id, latitude, longitude, name, is_live) VALUES('15551234567@s.whatsapp.net', 'loc1', 52.52, 13.405, 'Office', 0)`, nil},
		{`INSERT INTO messages(chat_jid, msg_id, sender_jid, ts, from_me, text) VALUES('999123456789@lid', 'lid1', '999123456789@lid', 1700000400, 0, 'hidden identity payload')`, nil},
		{`INSERT INTO messages(chat_jid, msg_id, sender_jid, ts, from_me, revoked, deleted_at, deletion_reason, payload_purged_at) VALUES('15551234567@s.whatsapp.net', 'purged1', '15551234567@s.whatsapp.net', 1700000300, 0, 1, 1700000310, 'whatsapp-revoke', 1700000320)`, nil},
		{`INSERT INTO message_payload_purges(chat_jid, msg_id, purged_at, deleted_at, deletion_reason) VALUES('15551234567@s.whatsapp.net', 'purged1', 1700000320, 1700000310, 'whatsapp-revoke')`, nil},
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s.sql, s.args...); err != nil {
			_ = raw.Close()
			t.Fatalf("populate v0.20.0 store (%s): %v", s.sql, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}
}

// TestOpenUpgradesPopulatedV0200Store opens a populated v0.20.0 store with
// current code and verifies the upgrade keeps every row, applies the new
// migrations in order, leaves ad_referral NULL on old rows, and preserves
// purge semantics.
func TestOpenUpgradesPopulatedV0200Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	buildPopulatedV0200Store(t, path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open v0.20.0 store with current code: %v", err)
	}
	defer db.Close()

	// New migrations applied in order, nothing skipped.
	var versions []int
	rows, err := db.sql.Query(`SELECT version FROM schema_migrations ORDER BY rowid`)
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan version: %v", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(versions) || len(versions) != len(schemaMigrations) || versions[len(versions)-1] != 30 {
		t.Fatalf("schema_migrations after upgrade = %v", versions)
	}

	// Every row survived the upgrade.
	for query, want := range map[string]int{
		`SELECT count(*) FROM chats`:                                  2,
		`SELECT count(*) FROM messages`:                               5,
		`SELECT count(*) FROM message_locations`:                      1,
		`SELECT count(*) FROM message_payload_purges`:                 1,
		`SELECT count(*) FROM messages WHERE chat_jid GLOB '*@lid'`:   1,
		`SELECT count(*) FROM messages WHERE ad_referral IS NOT NULL`: 0,
	} {
		var got int
		if err := db.sql.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}

	msg, err := db.GetMessage("15551234567@s.whatsapp.net", "plain1")
	if err != nil {
		t.Fatalf("GetMessage plain1: %v", err)
	}
	if msg.Text != "hello from v0.20.0" || msg.AdReferral != nil {
		t.Fatalf("plain1 after upgrade = %+v", msg)
	}
	tmpl, err := db.GetMessage("15551234567@s.whatsapp.net", "tmpl1")
	if err != nil {
		t.Fatalf("GetMessage tmpl1: %v", err)
	}
	if len(tmpl.Buttons) != 1 || tmpl.Buttons[0].DisplayText != "Buy flights" {
		t.Fatalf("tmpl1 buttons after upgrade = %+v", tmpl.Buttons)
	}
	lidMsg, err := db.GetMessage("999123456789@lid", "lid1")
	if err != nil {
		t.Fatalf("GetMessage lid1: %v", err)
	}
	if lidMsg.Text != "hidden identity payload" {
		t.Fatalf("lid1 after upgrade = %+v", lidMsg)
	}

	// Purge semantics unchanged: the tombstone stays and the payload stays gone.
	purged, err := db.GetMessage("15551234567@s.whatsapp.net", "purged1")
	if err != nil {
		t.Fatalf("GetMessage purged1: %v", err)
	}
	if purged.PayloadPurgedAt == nil || purged.Text != "" || purged.AdReferral != nil {
		t.Fatalf("purged1 after upgrade = %+v", purged)
	}
}

// TestReadOnlyOpenServesV0200StoreWithoutAdReferralColumn opens a populated
// v0.20.0 store read-only — no migrations run, so the ad_referral column does
// not exist — and verifies every message reader works and reports an absent
// referral: list (also the export projection), search, starred, show, and
// context before/after.
func TestReadOnlyOpenServesV0200StoreWithoutAdReferralColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	buildPopulatedV0200Store(t, path)

	db, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly v0.20.0 store: %v", err)
	}
	defer db.Close()

	requireNoReferral := func(label string, msgs []Message) {
		t.Helper()
		if len(msgs) == 0 {
			t.Fatalf("%s returned no messages", label)
		}
		for _, m := range msgs {
			if m.AdReferral != nil {
				t.Fatalf("%s returned a referral on a store without the column: %+v", label, m.AdReferral)
			}
		}
	}

	listed, err := db.ListMessages(ListMessagesParams{Limit: 100})
	if err != nil {
		t.Fatalf("ListMessages on read-only v0.20.0 store: %v", err)
	}
	requireNoReferral("list", listed)

	found, err := db.SearchMessages(SearchMessagesParams{Query: "hello", Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages on read-only v0.20.0 store: %v", err)
	}
	requireNoReferral("search", found)

	starred, err := db.ListStarredMessages(ListStarredMessagesParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListStarredMessages on read-only v0.20.0 store: %v", err)
	}
	requireNoReferral("starred", starred)

	shown, err := db.GetMessage("15551234567@s.whatsapp.net", "plain1")
	if err != nil {
		t.Fatalf("GetMessage on read-only v0.20.0 store: %v", err)
	}
	if shown.Text != "hello from v0.20.0" {
		t.Fatalf("GetMessage text = %q", shown.Text)
	}
	requireNoReferral("show", []Message{shown})

	around, err := db.MessageContext("15551234567@s.whatsapp.net", "tmpl1", 2, 2)
	if err != nil {
		t.Fatalf("MessageContext on read-only v0.20.0 store: %v", err)
	}
	if len(around) < 3 {
		t.Fatalf("MessageContext returned %d messages, want target plus neighbors", len(around))
	}
	requireNoReferral("context", around)
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

func TestOpenRepairsRecordedMessageLocationsMigrationMissingTable(t *testing.T) {
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
		DROP TABLE message_locations;
		INSERT OR IGNORE INTO schema_migrations(version, name, applied_at) VALUES(25, 'message locations', 1);
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

	if ok, err := db.tableExists("message_locations"); err != nil || !ok {
		t.Fatalf("message_locations exists=%v err=%v", ok, err)
	}
	if err := db.UpsertMessageLocation(MessageLocation{
		ChatJID: "15551112222@s.whatsapp.net", MsgID: "LOC-1", Latitude: 1, Longitude: 2,
	}); err != nil {
		t.Fatalf("UpsertMessageLocation after schema repair: %v", err)
	}
}
