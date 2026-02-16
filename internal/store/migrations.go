package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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
			time.Now().UTC().Unix(),
		); err != nil {
			return fmt.Errorf("record migration %03d: %w", m.version, err)
		}
	}

	return nil
}

func migrateCoreSchema(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS chats (
			jid TEXT PRIMARY KEY,
			kind TEXT NOT NULL, -- dm|group|broadcast|unknown
			name TEXT,
			last_message_ts INTEGER
		);

		CREATE TABLE IF NOT EXISTS contacts (
			jid TEXT PRIMARY KEY,
			phone TEXT,
			push_name TEXT,
			full_name TEXT,
			first_name TEXT,
			business_name TEXT,
			updated_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS groups (
			jid TEXT PRIMARY KEY,
			name TEXT,
			owner_jid TEXT,
			created_ts INTEGER,
			updated_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS group_participants (
			group_jid TEXT NOT NULL,
			user_jid TEXT NOT NULL,
			role TEXT,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (group_jid, user_jid),
			FOREIGN KEY (group_jid) REFERENCES groups(jid) ON DELETE CASCADE
		);

		CREATE TABLE IF NOT EXISTS contact_aliases (
			jid TEXT PRIMARY KEY,
			alias TEXT NOT NULL,
			notes TEXT,
			updated_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS contact_tags (
			jid TEXT NOT NULL,
			tag TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (jid, tag)
		);

		CREATE TABLE IF NOT EXISTS messages (
			rowid INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_jid TEXT NOT NULL,
			chat_name TEXT,
			msg_id TEXT NOT NULL,
			sender_jid TEXT,
			sender_name TEXT,
			ts INTEGER NOT NULL,
			from_me INTEGER NOT NULL,
			text TEXT,
			display_text TEXT,
			media_type TEXT,
			media_caption TEXT,
			filename TEXT,
			mime_type TEXT,
			direct_path TEXT,
			media_key BLOB,
			file_sha256 BLOB,
			file_enc_sha256 BLOB,
			file_length INTEGER,
			local_path TEXT,
			downloaded_at INTEGER,
			UNIQUE(chat_jid, msg_id),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, ts);
		CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts);
	`); err != nil {
		return fmt.Errorf("create tables: %w", err)
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

		CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
			INSERT INTO messages_fts(rowid, text, media_caption, filename, chat_name, sender_name, display_text)
			VALUES (new.rowid, COALESCE(new.text,''), COALESCE(new.media_caption,''), COALESCE(new.filename,''), COALESCE(new.chat_name,''), COALESCE(new.sender_name,''), COALESCE(new.display_text,''));
		END;

		CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN
			DELETE FROM messages_fts WHERE rowid = old.rowid;
		END;

		CREATE TRIGGER messages_au AFTER UPDATE ON messages BEGIN
			DELETE FROM messages_fts WHERE rowid = old.rowid;
			INSERT INTO messages_fts(rowid, text, media_caption, filename, chat_name, sender_name, display_text)
			VALUES (new.rowid, COALESCE(new.text,''), COALESCE(new.media_caption,''), COALESCE(new.filename,''), COALESCE(new.chat_name,''), COALESCE(new.sender_name,''), COALESCE(new.display_text,''));
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
