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
	for _, col := range []string{"reaction_to_id", "reaction_emoji"} {
		has, err := d.tableHasColumn("messages", col)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := d.sql.Exec(fmt.Sprintf("ALTER TABLE messages ADD COLUMN %s TEXT", col)); err != nil {
			return fmt.Errorf("add messages.%s column: %w", col, err)
		}
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
