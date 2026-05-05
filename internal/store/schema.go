package store

import "fmt"

const coreSchemaSQL = `
	CREATE TABLE IF NOT EXISTS chats (
		jid TEXT PRIMARY KEY,
		kind TEXT NOT NULL, -- dm|group|broadcast|unknown
		name TEXT,
		last_message_ts INTEGER,
		archived INTEGER NOT NULL DEFAULT 0,
		pinned INTEGER NOT NULL DEFAULT 0,
		muted_until INTEGER NOT NULL DEFAULT 0,
		unread INTEGER NOT NULL DEFAULT 0
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
		is_parent INTEGER NOT NULL DEFAULT 0,
		linked_parent_jid TEXT,
		left_at INTEGER,
		updated_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_groups_linked_parent_jid ON groups(linked_parent_jid);

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
		is_forwarded INTEGER NOT NULL DEFAULT 0,
		forwarding_score INTEGER NOT NULL DEFAULT 0,
		reaction_to_id TEXT,
		reaction_emoji TEXT,
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
		revoked INTEGER NOT NULL DEFAULT 0,
		deleted_for_me INTEGER NOT NULL DEFAULT 0,
		UNIQUE(chat_jid, msg_id),
		FOREIGN KEY (chat_jid) REFERENCES chats(jid) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, ts);
	CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts);

	CREATE TABLE IF NOT EXISTS starred (
		chat_jid TEXT NOT NULL,
		msg_id TEXT NOT NULL,
		sender_jid TEXT,
		from_me INTEGER NOT NULL DEFAULT 0,
		starred_at INTEGER NOT NULL,
		PRIMARY KEY (chat_jid, msg_id)
	);

	CREATE INDEX IF NOT EXISTS idx_starred_starred_at ON starred(starred_at);
`

func migrateCoreSchema(d *DB) error {
	if _, err := d.sql.Exec(coreSchemaSQL); err != nil {
		return fmt.Errorf("create tables: %w", err)
	}
	return nil
}
