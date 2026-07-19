package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type migration struct {
	version int
	name    string
	up      func(*DB) error
}

var schemaMigrations = []migration{
	{version: 1, name: "core schema", up: migrateCoreSchema},
	{version: 2, name: "messages display_text column", up: migrateMessagesDisplayText},
	{version: 3, name: "messages fts", up: migrateMessagesFTS},
	{version: 4, name: "groups left_at column", up: migrateGroupsLeftAt},
	{version: 5, name: "messages forwarded columns", up: migrateMessagesForwardedColumns},
	{version: 6, name: "messages reaction columns", up: migrateMessagesReactionColumns},
	{version: 7, name: "starred messages", up: migrateStarredMessages},
	{version: 8, name: "messages revoked column", up: migrateMessagesRevokedColumn},
	{version: 9, name: "messages deleted_for_me column", up: migrateMessagesDeletedForMeColumn},
	{version: 10, name: "chat state columns", up: migrateChatStateColumns},
	{version: 11, name: "group hierarchy columns", up: migrateGroupHierarchyColumns},
	{version: 12, name: "contacts system_name column", up: migrateContactsSystemNameColumn},
	{version: 13, name: "messages buttons column", up: migrateMessagesButtonsColumn},
	{version: 14, name: "polls and poll_votes", up: migratePolls},
	{version: 15, name: "call events", up: migrateCallEvents},
	{version: 16, name: "messages edited columns", up: migrateMessagesEditedColumns},
	{version: 17, name: "status messages", up: migrateStatusMessages},
	{version: 18, name: "chat unread count column", up: migrateChatUnreadCountColumn},
	{version: 19, name: "messages quoted columns", up: migrateMessagesQuotedColumns},
	{version: 20, name: "messages media_unavailable_at column", up: migrateMessagesMediaUnavailableColumn},
	{version: 21, name: "message tombstone metadata", up: migrateMessageTombstoneMetadata},
	{version: 22, name: "message local media aliases", up: ensureMessageLocalMediaAliasesTable},
	{version: 23, name: "app state recovery markers", up: migrateAppStateRecoveryMarkers},
	{version: 24, name: "app state recovery intents", up: migrateAppStateRecoveryIntents},
}

func ensureMessageLocalMediaAliasesTable(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS message_local_media_aliases (
			chat_jid TEXT NOT NULL,
			msg_id TEXT NOT NULL,
			local_path TEXT NOT NULL,
			downloaded_at INTEGER,
			PRIMARY KEY (chat_jid, msg_id, local_path),
			FOREIGN KEY (chat_jid, msg_id) REFERENCES messages(chat_jid, msg_id) ON DELETE CASCADE
		)
	`); err != nil {
		return fmt.Errorf("ensure message local media aliases: %w", err)
	}
	return nil
}

func migrateAppStateRecoveryMarkers(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS app_state_recovery_required (
			collection TEXT PRIMARY KEY,
			marked_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create app state recovery marker table: %w", err)
	}
	return nil
}

func migrateAppStateRecoveryIntents(d *DB) error {
	if err := reconcilePreReleaseAppStateMigrations(d); err != nil {
		return err
	}
	if err := migrateAppStateRecoveryMarkers(d); err != nil {
		return err
	}
	if err := ensureAppStateRecoveryIntents(d); err != nil {
		return err
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin app state recovery marker handoff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		INSERT INTO app_state_recovery_intents(collection)
		SELECT legacy.collection
		FROM app_state_recovery_required AS legacy
		WHERE NOT EXISTS (
			SELECT 1 FROM app_state_recovery_intents AS intent
			WHERE intent.collection = legacy.collection
		)
	`); err != nil {
		return fmt.Errorf("copy app state recovery markers to intents: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE app_state_recovery_required`); err != nil {
		return fmt.Errorf("drop migrated app state recovery markers: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit app state recovery marker handoff: %w", err)
	}
	return nil
}

func reconcilePreReleaseAppStateMigrations(d *DB) error {
	// Pre-release app-state builds used versions 21-23 before main assigned
	// versions 21 and 22 to message tombstones and local media aliases.
	var migrationName string
	err := d.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version = 21`).Scan(&migrationName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect pre-release app state migration: %w", err)
	}
	if err == nil && migrationName == "app state recovery markers" {
		if err := migrateMessageTombstoneMetadata(d); err != nil {
			return fmt.Errorf("reconcile pre-release message tombstone migration: %w", err)
		}
	}

	migrationName = ""
	err = d.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version = 22`).Scan(&migrationName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect pre-release app state migration: %w", err)
	}
	if err == nil && (migrationName == "app state recovery markers" || migrationName == "app state recovery intents") {
		if err := ensureMessageLocalMediaAliasesTable(d); err != nil {
			return fmt.Errorf("reconcile pre-release message local media aliases migration: %w", err)
		}
	}
	return nil
}

func ensureAppStateRecoveryIntents(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS app_state_recovery_intents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			collection TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_app_state_recovery_intents_collection
		ON app_state_recovery_intents(collection)
	`); err != nil {
		return fmt.Errorf("create app state recovery intent table: %w", err)
	}
	return nil
}

func migrateMessageTombstoneMetadata(d *DB) error {
	hasTable, err := d.tableExists("messages")
	if err != nil {
		return err
	}
	if !hasTable {
		return nil
	}
	if err := ensureMessageTombstoneColumns(d); err != nil {
		return err
	}
	if err := ensureMessagePayloadPurgesTable(d); err != nil {
		return err
	}
	if _, err := d.sql.Exec(`
		UPDATE messages
		SET deleted_at = COALESCE(deleted_at, ts),
			deletion_reason = COALESCE(deletion_reason, CASE
				WHEN deleted_for_me != 0 THEN 'legacy-whatsapp-delete-for-me'
				WHEN revoked != 0 THEN 'legacy-whatsapp-revoke'
			END)
		WHERE revoked != 0 OR deleted_for_me != 0
	`); err != nil {
		return fmt.Errorf("backfill message tombstone metadata: %w", err)
	}
	if _, err := d.sql.Exec(`
		INSERT INTO message_payload_purges(chat_jid, msg_id, purged_at, deleted_at, deletion_reason)
		SELECT chat_jid, msg_id, payload_purged_at, deleted_at, COALESCE(deletion_reason, 'explicit-payload-purge')
		FROM messages
		WHERE payload_purged_at IS NOT NULL
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			purged_at = min(message_payload_purges.purged_at, excluded.purged_at),
			deleted_at = min(message_payload_purges.deleted_at, excluded.deleted_at),
			deletion_reason = COALESCE(NULLIF(message_payload_purges.deletion_reason, ''), excluded.deletion_reason)
	`); err != nil {
		return fmt.Errorf("backfill message payload purge ledger: %w", err)
	}
	return migrateMessagesFTS(d)
}

func ensureMessageTombstoneColumns(d *DB) error {
	hasTable, err := d.tableExists("messages")
	if err != nil || !hasTable {
		return err
	}
	if err := addMessageColumnIfMissing(d, "deleted_at", `ALTER TABLE messages ADD COLUMN deleted_at INTEGER`); err != nil {
		return err
	}
	if err := addMessageColumnIfMissing(d, "deletion_reason", `ALTER TABLE messages ADD COLUMN deletion_reason TEXT`); err != nil {
		return err
	}
	return addMessageColumnIfMissing(d, "payload_purged_at", `ALTER TABLE messages ADD COLUMN payload_purged_at INTEGER`)
}

func ensureMessagePayloadPurgesTable(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS message_payload_purges (
			chat_jid TEXT NOT NULL,
			msg_id TEXT NOT NULL,
			purged_at INTEGER NOT NULL,
			deleted_at INTEGER NOT NULL,
			deletion_reason TEXT NOT NULL,
			PRIMARY KEY (chat_jid, msg_id)
		)
	`); err != nil {
		return fmt.Errorf("ensure message payload purge ledger: %w", err)
	}
	return nil
}

func addMessageColumnIfMissing(d *DB, column, ddl string) error {
	has, err := d.tableHasColumn("messages", column)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(ddl); err != nil {
		return fmt.Errorf("add messages.%s column: %w", column, err)
	}
	return nil
}

func migrateMessagesMediaUnavailableColumn(d *DB) error {
	hasTable, err := d.tableExists("messages")
	if err != nil {
		return err
	}
	if !hasTable {
		return nil
	}
	has, err := d.tableHasColumn("messages", "media_unavailable_at")
	if err != nil {
		return err
	}
	if !has {
		if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN media_unavailable_at INTEGER`); err != nil {
			return fmt.Errorf("add messages.media_unavailable_at column: %w", err)
		}
	}
	return nil
}

func (d *DB) ensureSchema() error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	applied := map[int]bool{}
	rows, err := d.sql.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("load applied migrations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied migrations: %w", err)
	}

	for _, m := range schemaMigrations {
		if applied[m.version] {
			continue
		}
		if err := m.up(d); err != nil {
			return fmt.Errorf("apply migration %03d %s: %w", m.version, m.name, err)
		}
		if _, err := d.sql.Exec(
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
			m.version,
			m.name,
			nowUTC().Unix(),
		); err != nil {
			return fmt.Errorf("record migration %03d: %w", m.version, err)
		}
	}

	return d.ensureCurrentSchema()
}

func (d *DB) ensureCurrentSchema() error {
	// Keep idempotent DDL guards here, not in query/write paths. This catches
	// local stores from interrupted or pre-release migrations where a version
	// row exists but the expected object does not.
	if err := migratePolls(d); err != nil {
		return fmt.Errorf("ensure current polls schema: %w", err)
	}
	if err := migrateCallEvents(d); err != nil {
		return fmt.Errorf("ensure current call events schema: %w", err)
	}
	if err := migrateMessagesEditedColumns(d); err != nil {
		return fmt.Errorf("ensure current messages edited columns: %w", err)
	}
	if err := migrateStatusMessages(d); err != nil {
		return fmt.Errorf("ensure current status messages schema: %w", err)
	}
	if err := migrateChatUnreadCountColumn(d); err != nil {
		return fmt.Errorf("ensure current chat unread count column: %w", err)
	}
	if err := migrateMessagesQuotedColumns(d); err != nil {
		return fmt.Errorf("ensure current messages quoted columns: %w", err)
	}
	if err := migrateMessagesMediaUnavailableColumn(d); err != nil {
		return fmt.Errorf("ensure current messages media unavailable column: %w", err)
	}
	if err := ensureMessageTombstoneColumns(d); err != nil {
		return fmt.Errorf("ensure current message tombstone columns: %w", err)
	}
	if err := ensureMessagePayloadPurgesTable(d); err != nil {
		return fmt.Errorf("ensure current message payload purge ledger: %w", err)
	}
	if err := ensureMessageLocalMediaAliasesTable(d); err != nil {
		return fmt.Errorf("ensure current message local media aliases: %w", err)
	}
	if err := ensureAppStateRecoveryIntents(d); err != nil {
		return fmt.Errorf("ensure current app state recovery intents: %w", err)
	}
	return nil
}

func migrateGroupsLeftAt(d *DB) error {
	hasLeftAt, err := d.tableHasColumn("groups", "left_at")
	if err != nil {
		return err
	}
	if hasLeftAt {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE groups ADD COLUMN left_at INTEGER`); err != nil {
		return fmt.Errorf("add groups.left_at column: %w", err)
	}
	return nil
}

func migrateMessagesDisplayText(d *DB) error {
	hasDisplayText, err := d.tableHasColumn("messages", "display_text")
	if err != nil {
		return err
	}
	if hasDisplayText {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN display_text TEXT`); err != nil {
		return fmt.Errorf("add display_text column: %w", err)
	}
	return nil
}

func migrateMessagesForwardedColumns(d *DB) error {
	hasForwarded, err := d.tableHasColumn("messages", "is_forwarded")
	if err != nil {
		return err
	}
	if !hasForwarded {
		if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN is_forwarded INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add messages.is_forwarded column: %w", err)
		}
	}

	hasScore, err := d.tableHasColumn("messages", "forwarding_score")
	if err != nil {
		return err
	}
	if !hasScore {
		if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN forwarding_score INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add messages.forwarding_score column: %w", err)
		}
	}
	return nil
}

func migrateMessagesReactionColumns(d *DB) error {
	if err := addTextColumnIfMissing(d, "reaction_to_id", `ALTER TABLE messages ADD COLUMN reaction_to_id TEXT`); err != nil {
		return err
	}
	if err := addTextColumnIfMissing(d, "reaction_emoji", `ALTER TABLE messages ADD COLUMN reaction_emoji TEXT`); err != nil {
		return err
	}
	return nil
}

func addTextColumnIfMissing(d *DB, col, stmt string) error {
	has, err := d.tableHasColumn("messages", col)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(stmt); err != nil {
		return fmt.Errorf("add messages.%s column: %w", col, err)
	}
	return nil
}

func migrateMessagesQuotedColumns(d *DB) error {
	hasTable, err := d.tableExists("messages")
	if err != nil {
		return err
	}
	if !hasTable {
		return nil
	}
	if err := addTextColumnIfMissing(d, "quoted_msg_id", `ALTER TABLE messages ADD COLUMN quoted_msg_id TEXT`); err != nil {
		return err
	}
	if err := addTextColumnIfMissing(d, "quoted_sender_jid", `ALTER TABLE messages ADD COLUMN quoted_sender_jid TEXT`); err != nil {
		return err
	}
	return nil
}

func migrateStarredMessages(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS starred (
			chat_jid TEXT NOT NULL,
			msg_id TEXT NOT NULL,
			sender_jid TEXT,
			from_me INTEGER NOT NULL DEFAULT 0,
			starred_at INTEGER NOT NULL,
			PRIMARY KEY (chat_jid, msg_id)
		);
		CREATE INDEX IF NOT EXISTS idx_starred_starred_at ON starred(starred_at);
	`); err != nil {
		return fmt.Errorf("create starred table: %w", err)
	}
	return nil
}

func migrateMessagesRevokedColumn(d *DB) error {
	hasRevoked, err := d.tableHasColumn("messages", "revoked")
	if err != nil {
		return err
	}
	if !hasRevoked {
		if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN revoked INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add messages.revoked column: %w", err)
		}
	}
	return migrateMessagesFTS(d)
}

func migrateMessagesDeletedForMeColumn(d *DB) error {
	hasDeletedForMe, err := d.tableHasColumn("messages", "deleted_for_me")
	if err != nil {
		return err
	}
	if !hasDeletedForMe {
		if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN deleted_for_me INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add messages.deleted_for_me column: %w", err)
		}
	}
	return migrateMessagesFTS(d)
}

func migrateChatStateColumns(d *DB) error {
	cols := []struct {
		name string
		ddl  string
	}{
		{"archived", "ALTER TABLE chats ADD COLUMN archived INTEGER NOT NULL DEFAULT 0"},
		{"pinned", "ALTER TABLE chats ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0"},
		{"muted_until", "ALTER TABLE chats ADD COLUMN muted_until INTEGER NOT NULL DEFAULT 0"},
		{"unread", "ALTER TABLE chats ADD COLUMN unread INTEGER NOT NULL DEFAULT 0"},
	}
	for _, col := range cols {
		has, err := d.tableHasColumn("chats", col.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(col.ddl); err != nil {
			return fmt.Errorf("add chats.%s column: %w", col.name, err)
		}
	}
	return nil
}

func migrateChatUnreadCountColumn(d *DB) error {
	hasChats, err := d.tableExists("chats")
	if err != nil {
		return err
	}
	if !hasChats {
		return nil
	}
	hasUnreadCount, err := d.tableHasColumn("chats", "unread_count")
	if err != nil {
		return err
	}
	if hasUnreadCount {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE chats ADD COLUMN unread_count INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add chats.unread_count column: %w", err)
	}
	hasUnread, err := d.tableHasColumn("chats", "unread")
	if err != nil {
		return err
	}
	if !hasUnread {
		return nil
	}
	if _, err := d.sql.Exec(`
		UPDATE chats
		SET unread_count = CASE
			WHEN COALESCE(unread, 0) > 0 THEN COALESCE(unread, 0)
			ELSE 0
		END
	`); err != nil {
		return fmt.Errorf("migrate chats unread counts: %w", err)
	}
	if _, err := d.sql.Exec(`
		UPDATE chats
		SET unread = CASE WHEN COALESCE(unread, 0) != 0 THEN 1 ELSE 0 END
	`); err != nil {
		return fmt.Errorf("migrate chats unread markers: %w", err)
	}
	return nil
}

func migrateGroupHierarchyColumns(d *DB) error {
	cols := []struct {
		name string
		ddl  string
	}{
		{"is_parent", "ALTER TABLE groups ADD COLUMN is_parent INTEGER NOT NULL DEFAULT 0"},
		{"linked_parent_jid", "ALTER TABLE groups ADD COLUMN linked_parent_jid TEXT"},
	}
	for _, col := range cols {
		has, err := d.tableHasColumn("groups", col.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(col.ddl); err != nil {
			return fmt.Errorf("add groups.%s column: %w", col.name, err)
		}
	}
	if _, err := d.sql.Exec(`CREATE INDEX IF NOT EXISTS idx_groups_linked_parent_jid ON groups(linked_parent_jid)`); err != nil {
		return fmt.Errorf("create groups linked-parent index: %w", err)
	}
	return nil
}

func migrateMessagesButtonsColumn(d *DB) error {
	hasTable, err := d.tableExists("messages")
	if err != nil {
		return err
	}
	if !hasTable {
		return nil
	}
	has, err := d.tableHasColumn("messages", "buttons")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN buttons TEXT`); err != nil {
		return fmt.Errorf("add messages.buttons column: %w", err)
	}
	return nil
}

func migrateMessagesEditedColumns(d *DB) error {
	hasTable, err := d.tableExists("messages")
	if err != nil {
		return err
	}
	if !hasTable {
		return nil
	}
	has, err := d.tableHasColumn("messages", "edited")
	if err != nil {
		return err
	}
	if !has {
		if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN edited INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add messages.edited column: %w", err)
		}
	}
	has, err = d.tableHasColumn("messages", "edited_ts")
	if err != nil {
		return err
	}
	if !has {
		if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN edited_ts INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add messages.edited_ts column: %w", err)
		}
	}
	return nil
}

func migratePolls(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS polls (
			chat_jid          TEXT NOT NULL,
			msg_id            TEXT NOT NULL,
			sender_jid        TEXT,
			question          TEXT NOT NULL,
			options_json      TEXT NOT NULL,
			selectable_count  INTEGER NOT NULL DEFAULT 1,
			created_ts        INTEGER NOT NULL,
			PRIMARY KEY (chat_jid, msg_id)
		);
		CREATE INDEX IF NOT EXISTS idx_polls_chat_ts ON polls(chat_jid, created_ts);

		CREATE TABLE IF NOT EXISTS poll_votes (
			chat_jid              TEXT NOT NULL,
			poll_msg_id           TEXT NOT NULL,
			voter_jid             TEXT NOT NULL,
			vote_msg_id           TEXT NOT NULL,
			selected_options_json TEXT NOT NULL,
			ts                    INTEGER NOT NULL,
			PRIMARY KEY (chat_jid, poll_msg_id, voter_jid)
		);
		CREATE INDEX IF NOT EXISTS idx_poll_votes_poll ON poll_votes(chat_jid, poll_msg_id);
	`); err != nil {
		return fmt.Errorf("create polls tables: %w", err)
	}
	return nil
}

func migrateCallEvents(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS call_events (
			rowid INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_jid TEXT NOT NULL,
			chat_name TEXT,
			sender_jid TEXT,
			sender_name TEXT,
			call_id TEXT NOT NULL,
			msg_id TEXT,
			event_type TEXT NOT NULL,
			direction TEXT,
			media TEXT,
			outcome TEXT,
			reason TEXT,
			call_type TEXT,
			duration_secs INTEGER NOT NULL DEFAULT 0,
			ts INTEGER NOT NULL,
			participants TEXT,
			UNIQUE(chat_jid, call_id, event_type, ts),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_call_events_chat_ts ON call_events(chat_jid, ts);
		CREATE INDEX IF NOT EXISTS idx_call_events_ts ON call_events(ts);
	`); err != nil {
		return fmt.Errorf("create call_events table: %w", err)
	}
	return nil
}

func migrateStatusMessages(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS status_messages (
			rowid INTEGER PRIMARY KEY AUTOINCREMENT,
			msg_id TEXT NOT NULL UNIQUE,
			ts INTEGER NOT NULL,
			from_me INTEGER NOT NULL,
			sender_jid TEXT,
			sender_name TEXT,
			text TEXT,
			media_type TEXT,
			media_caption TEXT,
			filename TEXT,
			mime_type TEXT,
			direct_path TEXT,
			media_key BLOB,
			file_sha256 BLOB,
			file_enc_sha256 BLOB,
			file_length INTEGER,
			background_color TEXT,
			font INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_status_messages_ts ON status_messages(ts);
	`); err != nil {
		return fmt.Errorf("create status_messages table: %w", err)
	}
	return migrateStatusMessageMediaColumns(d)
}

func migrateContactsSystemNameColumn(d *DB) error {
	hasContacts, err := d.tableExists("contacts")
	if err != nil {
		return err
	}
	if !hasContacts {
		return nil
	}
	hasSystemName, err := d.tableHasColumn("contacts", "system_name")
	if err != nil {
		return err
	}
	if hasSystemName {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE contacts ADD COLUMN system_name TEXT`); err != nil {
		return fmt.Errorf("add contacts.system_name column: %w", err)
	}
	return nil
}

func migrateMessagesFTS(d *DB) error {
	ftsExists, err := d.tableExists("messages_fts")
	if err != nil {
		return err
	}
	if ftsExists {
		hasDisplay, err := d.tableHasColumn("messages_fts", "display_text")
		if err != nil {
			return err
		}
		if !hasDisplay {
			if _, err := d.sql.Exec(`DROP TABLE IF EXISTS messages_fts`); err != nil {
				return fmt.Errorf("drop messages_fts: %w", err)
			}
			ftsExists = false
		}
	}

	created := false
	if !ftsExists {
		if _, err := d.sql.Exec(`
			CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
				text,
				media_caption,
				filename,
				chat_name,
				sender_name,
				display_text
			)
		`); err != nil {
			// Continue without FTS (fallback to LIKE).
			d.ftsEnabled = false
			return nil
		}
		created = true
	}

	// Ensure triggers match expected semantics.
	if _, err := d.sql.Exec(`
		DROP TRIGGER IF EXISTS messages_ai;
		DROP TRIGGER IF EXISTS messages_ad;
		DROP TRIGGER IF EXISTS messages_au;

		CREATE TRIGGER messages_ai AFTER INSERT ON messages WHEN new.deleted_at IS NULL BEGIN
			INSERT INTO messages_fts(rowid, text, media_caption, filename, chat_name, sender_name, display_text)
			VALUES (new.rowid, COALESCE(new.text,''), COALESCE(new.media_caption,''), COALESCE(new.filename,''), COALESCE(new.chat_name,''), COALESCE(new.sender_name,''), COALESCE(new.display_text,''));
		END;

		CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
			DELETE FROM messages_fts WHERE rowid = old.rowid;
		END;

		CREATE TRIGGER messages_au AFTER UPDATE ON messages BEGIN
			DELETE FROM messages_fts WHERE rowid = old.rowid;
			INSERT INTO messages_fts(rowid, text, media_caption, filename, chat_name, sender_name, display_text)
			SELECT new.rowid, COALESCE(new.text,''), COALESCE(new.media_caption,''), COALESCE(new.filename,''), COALESCE(new.chat_name,''), COALESCE(new.sender_name,''), COALESCE(new.display_text,'')
			WHERE new.deleted_at IS NULL;
		END;
	`); err != nil {
		d.ftsEnabled = false
		return nil
	}

	if created {
		if _, err := d.sql.Exec(`
			INSERT INTO messages_fts(rowid, text, media_caption, filename, chat_name, sender_name, display_text)
			SELECT rowid,
			       COALESCE(text,''),
			       COALESCE(media_caption,''),
			       COALESCE(filename,''),
			       COALESCE(chat_name,''),
			       COALESCE(sender_name,''),
			       COALESCE(display_text,'')
			FROM messages
			WHERE deleted_at IS NULL
		`); err != nil {
			d.ftsEnabled = false
			return nil
		}
	}

	d.ftsEnabled = true
	return nil
}

func (d *DB) tableExists(table string) (bool, error) {
	row := d.sql.QueryRow(`SELECT 1 FROM sqlite_master WHERE name = ? AND type IN ('table','view')`, table)
	var one int
	if err := row.Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (d *DB) tableHasColumn(table, column string) (bool, error) {
	query, err := tableInfoPragma(table)
	if err != nil {
		return false, fmt.Errorf("tableHasColumn table: %w", err)
	}
	if !isSQLiteIdentifier(column) {
		return false, fmt.Errorf("tableHasColumn column: invalid SQLite identifier %q", column)
	}
	rows, err := d.sql.Query(query)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid     int
			name    string
			colType string
			notNull int
			pk      int
			dflt    sql.NullString
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func tableInfoPragma(table string) (string, error) {
	switch table {
	case "chats":
		return `PRAGMA table_info("chats")`, nil
	case "contacts":
		return `PRAGMA table_info("contacts")`, nil
	case "groups":
		return `PRAGMA table_info("groups")`, nil
	case "messages":
		return `PRAGMA table_info("messages")`, nil
	case "messages_fts":
		return `PRAGMA table_info("messages_fts")`, nil
	case "status_messages":
		return `PRAGMA table_info("status_messages")`, nil
	default:
		return "", fmt.Errorf("unsupported table %q", table)
	}
}

func isSQLiteIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') {
			continue
		}
		if i > 0 && '0' <= c && c <= '9' {
			continue
		}
		return false
	}
	return true
}
