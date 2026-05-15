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

		CREATE TRIGGER messages_ai AFTER INSERT ON messages WHEN new.revoked = 0 AND new.deleted_for_me = 0 BEGIN
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
			WHERE new.revoked = 0 AND new.deleted_for_me = 0;
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
			WHERE revoked = 0 AND deleted_for_me = 0
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
	// table is always a hardcoded identifier at call sites; validate to prevent
	// accidental misuse with user-controlled input (#58).
	if table == "" {
		return false, fmt.Errorf("tableHasColumn: table name is required")
	}
	rows, err := d.sql.Query("PRAGMA table_info(" + table + ")")
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
