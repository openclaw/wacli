package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	path       string
	sql        *sql.DB
	ftsEnabled bool
}

func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on&_busy_timeout=5000", path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	s := &DB{path: path, sql: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

func (d *DB) init() error {
	// Pragmas: keep consistent for writers/readers.
	_, _ = d.sql.Exec("PRAGMA journal_mode=WAL;")
	_, _ = d.sql.Exec("PRAGMA synchronous=NORMAL;")
	_, _ = d.sql.Exec("PRAGMA temp_store=MEMORY;")
	_, _ = d.sql.Exec("PRAGMA foreign_keys=ON;")

	if err := d.ensureSchema(); err != nil {
		return err
	}
	return nil
}

func (d *DB) ensureSchema() error {
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

	if err := d.ensureMessageColumns(); err != nil {
		return err
	}

	if err := d.ensureContactColumns(); err != nil {
		return err
	}

	if err := d.ensureMessagesFTS(); err != nil {
		return err
	}

	return nil
}

func (d *DB) ensureContactColumns() error {
	ok, err := d.tableHasColumn("contacts", "system_name")
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE contacts ADD COLUMN system_name TEXT`); err != nil {
		return fmt.Errorf("add system_name column: %w", err)
	}
	return nil
}

func (d *DB) ensureMessageColumns() error {
	ok, err := d.tableHasColumn("messages", "display_text")
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if _, err := d.sql.Exec(`ALTER TABLE messages ADD COLUMN display_text TEXT`); err != nil {
		return fmt.Errorf("add display_text column: %w", err)
	}
	return nil
}

func (d *DB) ensureMessagesFTS() error {
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
			);
		`); err != nil {
			// Continue without FTS (fallback to LIKE).
			d.ftsEnabled = false
			return nil
		}
		created = true
	}

	// Ensure triggers match our expected semantics (FTS5 supports DELETE directly).
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
			FROM messages;
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
		var cid int
		var name string
		var colType string
		var notNull int
		var pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// --- domain types + helpers

type Chat struct {
	JID           string
	Kind          string
	Name          string
	LastMessageTS time.Time
}

type Group struct {
	JID       string
	Name      string
	OwnerJID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type GroupParticipant struct {
	GroupJID  string
	UserJID   string
	Role      string
	UpdatedAt time.Time
}

type MediaDownloadInfo struct {
	ChatJID       string
	ChatName      string
	MsgID         string
	MediaType     string
	Filename      string
	MimeType      string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
	LocalPath     string
	DownloadedAt  time.Time
}

type Message struct {
	ChatJID     string
	ChatName    string
	MsgID       string
	SenderJID   string
	Timestamp   time.Time
	FromMe      bool
	Text        string
	DisplayText string
	MediaType   string
	Snippet     string
}

type MessageInfo struct {
	ChatJID    string
	MsgID      string
	Timestamp  time.Time
	FromMe     bool
	SenderJID  string
	SenderName string
}

type Contact struct {
	JID        string
	Phone      string
	Name       string    // Display name (computed from precedence: alias > system_name > whatsapp names)
	Alias      string    // Manual user override
	SystemName string    // From system contacts (macOS/iOS address book)
	Tags       []string
	UpdatedAt  time.Time
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

func fromUnix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (d *DB) UpsertChat(jid, kind, name string, lastTS time.Time) error {
	if strings.TrimSpace(kind) == "" {
		kind = "unknown"
	}
	_, err := d.sql.Exec(`
		INSERT INTO chats(jid, kind, name, last_message_ts)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			kind=excluded.kind,
			name=CASE WHEN excluded.name IS NOT NULL AND excluded.name != '' THEN excluded.name ELSE chats.name END,
			last_message_ts=CASE WHEN excluded.last_message_ts > COALESCE(chats.last_message_ts, 0) THEN excluded.last_message_ts ELSE chats.last_message_ts END
	`, jid, kind, name, unix(lastTS))
	return err
}

type UpsertMessageParams struct {
	ChatJID       string
	ChatName      string
	MsgID         string
	SenderJID     string
	SenderName    string
	Timestamp     time.Time
	FromMe        bool
	Text          string
	DisplayText   string
	MediaType     string
	MediaCaption  string
	Filename      string
	MimeType      string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
}

func (d *DB) UpsertMessage(p UpsertMessageParams) error {
	_, err := d.sql.Exec(`
		INSERT INTO messages(
			chat_jid, chat_name, msg_id, sender_jid, sender_name, ts, from_me, text, display_text,
			media_type, media_caption, filename, mime_type, direct_path,
			media_key, file_sha256, file_enc_sha256, file_length
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			chat_name=COALESCE(NULLIF(excluded.chat_name,''), messages.chat_name),
			sender_jid=excluded.sender_jid,
			sender_name=COALESCE(NULLIF(excluded.sender_name,''), messages.sender_name),
			ts=excluded.ts,
			from_me=excluded.from_me,
			text=excluded.text,
			display_text=CASE WHEN excluded.display_text IS NOT NULL AND excluded.display_text != '' THEN excluded.display_text ELSE messages.display_text END,
			media_type=excluded.media_type,
			media_caption=excluded.media_caption,
			filename=COALESCE(NULLIF(excluded.filename,''), messages.filename),
			mime_type=COALESCE(NULLIF(excluded.mime_type,''), messages.mime_type),
			direct_path=COALESCE(NULLIF(excluded.direct_path,''), messages.direct_path),
			media_key=CASE WHEN excluded.media_key IS NOT NULL AND length(excluded.media_key)>0 THEN excluded.media_key ELSE messages.media_key END,
			file_sha256=CASE WHEN excluded.file_sha256 IS NOT NULL AND length(excluded.file_sha256)>0 THEN excluded.file_sha256 ELSE messages.file_sha256 END,
			file_enc_sha256=CASE WHEN excluded.file_enc_sha256 IS NOT NULL AND length(excluded.file_enc_sha256)>0 THEN excluded.file_enc_sha256 ELSE messages.file_enc_sha256 END,
			file_length=CASE WHEN excluded.file_length>0 THEN excluded.file_length ELSE messages.file_length END
	`, p.ChatJID, nullIfEmpty(p.ChatName), p.MsgID, nullIfEmpty(p.SenderJID), nullIfEmpty(p.SenderName), unix(p.Timestamp), boolToInt(p.FromMe), nullIfEmpty(p.Text), nullIfEmpty(p.DisplayText),
		nullIfEmpty(p.MediaType), nullIfEmpty(p.MediaCaption), nullIfEmpty(p.Filename), nullIfEmpty(p.MimeType), nullIfEmpty(p.DirectPath),
		p.MediaKey, p.FileSHA256, p.FileEncSHA256, int64(p.FileLength),
	)
	return err
}

func nullIfEmpty(s string) interface{} {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

type ListMessagesParams struct {
	ChatJID string
	Limit   int
	Before  *time.Time
	After   *time.Time
}

func (d *DB) ListMessages(p ListMessagesParams) ([]Message, error) {
	if p.Limit <= 0 {
		p.Limit = 50
	}
	query := `
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,'')
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE 1=1`
	var args []interface{}
	if strings.TrimSpace(p.ChatJID) != "" {
		query += " AND m.chat_jid = ?"
		args = append(args, p.ChatJID)
	}
	if p.After != nil {
		query += " AND m.ts > ?"
		args = append(args, unix(*p.After))
	}
	if p.Before != nil {
		query += " AND m.ts < ?"
		args = append(args, unix(*p.Before))
	}
	query += " ORDER BY m.ts DESC LIMIT ?"
	args = append(args, p.Limit)

	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		var ts int64
		var fromMe int
		if err := rows.Scan(&m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &ts, &fromMe, &m.Text, &m.DisplayText, &m.MediaType); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.FromMe = fromMe != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

type SearchMessagesParams struct {
	Query   string
	ChatJID string
	From    string
	Limit   int
	Before  *time.Time
	After   *time.Time
	Type    string
}

func (d *DB) SearchMessages(p SearchMessagesParams) ([]Message, error) {
	if strings.TrimSpace(p.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}

	if d.ftsEnabled {
		return d.searchFTS(p)
	}
	return d.searchLIKE(p)
}

func (d *DB) searchLIKE(p SearchMessagesParams) ([]Message, error) {
	query := `
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''), ''
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE (LOWER(m.text) LIKE LOWER(?) OR LOWER(m.display_text) LIKE LOWER(?) OR LOWER(m.media_caption) LIKE LOWER(?) OR LOWER(m.filename) LIKE LOWER(?) OR LOWER(COALESCE(m.chat_name,'')) LIKE LOWER(?) OR LOWER(COALESCE(m.sender_name,'')) LIKE LOWER(?) OR LOWER(COALESCE(c.name,'')) LIKE LOWER(?))`
	needle := "%" + p.Query + "%"
	args := []interface{}{needle, needle, needle, needle, needle, needle, needle}
	query, args = applyMessageFilters(query, args, p)
	query += " ORDER BY m.ts DESC LIMIT ?"
	args = append(args, p.Limit)
	return d.scanMessages(query, args...)
}

func (d *DB) searchFTS(p SearchMessagesParams) ([]Message, error) {
	query := `
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''),
		       snippet(messages_fts, 0, '[', ']', '…', 12)
		FROM messages_fts
		JOIN messages m ON messages_fts.rowid = m.rowid
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE messages_fts MATCH ?`
	args := []interface{}{p.Query}
	query, args = applyMessageFilters(query, args, p)
	query += " ORDER BY bm25(messages_fts) LIMIT ?"
	args = append(args, p.Limit)
	return d.scanMessages(query, args...)
}

func applyMessageFilters(query string, args []interface{}, p SearchMessagesParams) (string, []interface{}) {
	if strings.TrimSpace(p.ChatJID) != "" {
		query += " AND m.chat_jid = ?"
		args = append(args, p.ChatJID)
	}
	if strings.TrimSpace(p.From) != "" {
		query += " AND m.sender_jid = ?"
		args = append(args, p.From)
	}
	if p.After != nil {
		query += " AND m.ts > ?"
		args = append(args, unix(*p.After))
	}
	if p.Before != nil {
		query += " AND m.ts < ?"
		args = append(args, unix(*p.Before))
	}
	if strings.TrimSpace(p.Type) != "" {
		query += " AND COALESCE(m.media_type,'') = ?"
		args = append(args, p.Type)
	}
	return query, args
}

func (d *DB) scanMessages(query string, args ...interface{}) ([]Message, error) {
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		var m Message
		var ts int64
		var fromMe int
		if err := rows.Scan(&m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &ts, &fromMe, &m.Text, &m.DisplayText, &m.MediaType, &m.Snippet); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.FromMe = fromMe != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) GetMessage(chatJID, msgID string) (Message, error) {
	row := d.sql.QueryRow(`
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,'')
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ? AND m.msg_id = ?
	`, chatJID, msgID)
	var m Message
	var ts int64
	var fromMe int
	if err := row.Scan(&m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &ts, &fromMe, &m.Text, &m.DisplayText, &m.MediaType); err != nil {
		return Message{}, err
	}
	m.Timestamp = fromUnix(ts)
	m.FromMe = fromMe != 0
	return m, nil
}

func (d *DB) CountMessages() (int64, error) {
	row := d.sql.QueryRow(`SELECT COUNT(1) FROM messages`)
	var n int64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (d *DB) GetOldestMessageInfo(chatJID string) (MessageInfo, error) {
	chatJID = strings.TrimSpace(chatJID)
	if chatJID == "" {
		return MessageInfo{}, fmt.Errorf("chat JID is required")
	}
	row := d.sql.QueryRow(`
		SELECT m.chat_jid, m.msg_id, m.ts, m.from_me, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,'')
		FROM messages m
		WHERE m.chat_jid = ?
		ORDER BY m.ts ASC
		LIMIT 1
	`, chatJID)
	var out MessageInfo
	var ts int64
	var fromMe int
	if err := row.Scan(&out.ChatJID, &out.MsgID, &ts, &fromMe, &out.SenderJID, &out.SenderName); err != nil {
		return MessageInfo{}, err
	}
	out.Timestamp = fromUnix(ts)
	out.FromMe = fromMe != 0
	return out, nil
}

func (d *DB) GetMediaDownloadInfo(chatJID, msgID string) (MediaDownloadInfo, error) {
	row := d.sql.QueryRow(`
		SELECT m.chat_jid,
		       COALESCE(c.name,''),
		       m.msg_id,
		       COALESCE(m.media_type,''),
		       COALESCE(m.filename,''),
		       COALESCE(m.mime_type,''),
		       COALESCE(m.direct_path,''),
		       m.media_key,
		       m.file_sha256,
		       m.file_enc_sha256,
		       COALESCE(m.file_length,0),
		       COALESCE(m.local_path,''),
		       COALESCE(m.downloaded_at,0)
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ? AND m.msg_id = ?
	`, chatJID, msgID)

	var info MediaDownloadInfo
	var fileLen sql.NullInt64
	var downloadedAt int64
	if err := row.Scan(
		&info.ChatJID,
		&info.ChatName,
		&info.MsgID,
		&info.MediaType,
		&info.Filename,
		&info.MimeType,
		&info.DirectPath,
		&info.MediaKey,
		&info.FileSHA256,
		&info.FileEncSHA256,
		&fileLen,
		&info.LocalPath,
		&downloadedAt,
	); err != nil {
		return MediaDownloadInfo{}, err
	}
	if fileLen.Valid && fileLen.Int64 > 0 {
		info.FileLength = uint64(fileLen.Int64)
	}
	info.DownloadedAt = fromUnix(downloadedAt)
	return info, nil
}

func (d *DB) MarkMediaDownloaded(chatJID, msgID, localPath string, downloadedAt time.Time) error {
	_, err := d.sql.Exec(`
		UPDATE messages
		SET local_path = ?, downloaded_at = ?
		WHERE chat_jid = ? AND msg_id = ?
	`, localPath, unix(downloadedAt), chatJID, msgID)
	return err
}

func (d *DB) MessageContext(chatJID, msgID string, before, after int) ([]Message, error) {
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	target, err := d.GetMessage(chatJID, msgID)
	if err != nil {
		return nil, err
	}

	beforeRows, err := d.sql.Query(`
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''), ''
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ? AND m.ts < ?
		ORDER BY m.ts DESC
		LIMIT ?
	`, chatJID, unix(target.Timestamp), before)
	if err != nil {
		return nil, err
	}
	defer beforeRows.Close()

	var prev []Message
	for beforeRows.Next() {
		var m Message
		var ts int64
		var fromMe int
		if err := beforeRows.Scan(&m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &ts, &fromMe, &m.Text, &m.DisplayText, &m.MediaType, &m.Snippet); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.FromMe = fromMe != 0
		prev = append(prev, m)
	}
	if err := beforeRows.Err(); err != nil {
		return nil, err
	}

	afterRows, err := d.sql.Query(`
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''), ''
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ? AND m.ts > ?
		ORDER BY m.ts ASC
		LIMIT ?
	`, chatJID, unix(target.Timestamp), after)
	if err != nil {
		return nil, err
	}
	defer afterRows.Close()

	var next []Message
	for afterRows.Next() {
		var m Message
		var ts int64
		var fromMe int
		if err := afterRows.Scan(&m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &ts, &fromMe, &m.Text, &m.DisplayText, &m.MediaType, &m.Snippet); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.FromMe = fromMe != 0
		next = append(next, m)
	}
	if err := afterRows.Err(); err != nil {
		return nil, err
	}

	// Reverse prev to chronological order.
	for i, j := 0, len(prev)-1; i < j; i, j = i+1, j-1 {
		prev[i], prev[j] = prev[j], prev[i]
	}

	out := make([]Message, 0, len(prev)+1+len(next))
	out = append(out, prev...)
	out = append(out, target)
	out = append(out, next...)
	return out, nil
}

func (d *DB) ListChats(query string, limit int) ([]Chat, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT jid, kind, COALESCE(name,''), COALESCE(last_message_ts,0) FROM chats WHERE 1=1`
	var args []interface{}
	if strings.TrimSpace(query) != "" {
		q += ` AND (LOWER(name) LIKE LOWER(?) OR LOWER(jid) LIKE LOWER(?))`
		needle := "%" + query + "%"
		args = append(args, needle, needle)
	}
	q += ` ORDER BY last_message_ts DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &ts); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) GetChat(jid string) (Chat, error) {
	row := d.sql.QueryRow(`SELECT jid, kind, COALESCE(name,''), COALESCE(last_message_ts,0) FROM chats WHERE jid = ?`, jid)
	var c Chat
	var ts int64
	if err := row.Scan(&c.JID, &c.Kind, &c.Name, &ts); err != nil {
		return Chat{}, err
	}
	c.LastMessageTS = fromUnix(ts)
	return c, nil
}

func (d *DB) SearchContacts(query string, limit int) ([]Contact, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 50
	}
	// Display name precedence: alias > system_name > full_name > push_name > business_name > first_name
	q := `
		SELECT c.jid,
		       COALESCE(c.phone,''),
		       COALESCE(NULLIF(a.alias,''), ''),
		       COALESCE(NULLIF(c.system_name,''), ''),
		       COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), ''),
		       c.updated_at
		FROM contacts c
		LEFT JOIN contact_aliases a ON a.jid = c.jid
		WHERE LOWER(COALESCE(a.alias,'')) LIKE LOWER(?) 
		   OR LOWER(COALESCE(c.system_name,'')) LIKE LOWER(?)
		   OR LOWER(COALESCE(c.full_name,'')) LIKE LOWER(?) 
		   OR LOWER(COALESCE(c.push_name,'')) LIKE LOWER(?) 
		   OR LOWER(COALESCE(c.phone,'')) LIKE LOWER(?) 
		   OR LOWER(c.jid) LIKE LOWER(?)
		ORDER BY COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), c.jid)
		LIMIT ?`
	needle := "%" + query + "%"
	rows, err := d.sql.Query(q, needle, needle, needle, needle, needle, needle, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		var updated int64
		if err := rows.Scan(&c.JID, &c.Phone, &c.Alias, &c.SystemName, &c.Name, &updated); err != nil {
			return nil, err
		}
		c.UpdatedAt = fromUnix(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) GetContact(jid string) (Contact, error) {
	// Display name precedence: alias > system_name > full_name > push_name > business_name > first_name
	row := d.sql.QueryRow(`
		SELECT c.jid,
		       COALESCE(c.phone,''),
		       COALESCE(NULLIF(a.alias,''), ''),
		       COALESCE(NULLIF(c.system_name,''), ''),
		       COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), ''),
		       c.updated_at
		FROM contacts c
		LEFT JOIN contact_aliases a ON a.jid = c.jid
		WHERE c.jid = ?
	`, jid)
	var c Contact
	var updated int64
	if err := row.Scan(&c.JID, &c.Phone, &c.Alias, &c.SystemName, &c.Name, &updated); err != nil {
		return Contact{}, err
	}
	c.UpdatedAt = fromUnix(updated)
	tags, _ := d.ListTags(jid)
	c.Tags = tags
	return c, nil
}

func (d *DB) ListTags(jid string) ([]string, error) {
	rows, err := d.sql.Query(`SELECT tag FROM contact_tags WHERE jid = ? ORDER BY tag`, jid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (d *DB) UpsertContact(jid, phone, pushName, fullName, firstName, businessName string) error {
	now := time.Now().UTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO contacts(jid, phone, push_name, full_name, first_name, business_name, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			phone=COALESCE(NULLIF(excluded.phone,''), contacts.phone),
			push_name=COALESCE(NULLIF(excluded.push_name,''), contacts.push_name),
			full_name=COALESCE(NULLIF(excluded.full_name,''), contacts.full_name),
			first_name=COALESCE(NULLIF(excluded.first_name,''), contacts.first_name),
			business_name=COALESCE(NULLIF(excluded.business_name,''), contacts.business_name),
			updated_at=excluded.updated_at
	`, jid, phone, pushName, fullName, firstName, businessName, now)
	return err
}

func (d *DB) UpsertGroup(jid, name, ownerJID string, created time.Time) error {
	now := time.Now().UTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO groups(jid, name, owner_jid, created_ts, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			name=COALESCE(NULLIF(excluded.name,''), groups.name),
			owner_jid=COALESCE(NULLIF(excluded.owner_jid,''), groups.owner_jid),
			created_ts=COALESCE(NULLIF(excluded.created_ts,0), groups.created_ts),
			updated_at=excluded.updated_at
	`, jid, name, ownerJID, unix(created), now)
	return err
}

func (d *DB) ReplaceGroupParticipants(groupJID string, participants []GroupParticipant) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM group_participants WHERE group_jid = ?`, groupJID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO group_participants(group_jid, user_jid, role, updated_at) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC()
	for _, p := range participants {
		role := strings.TrimSpace(p.Role)
		if role == "" {
			role = "member"
		}
		if _, err = stmt.Exec(groupJID, p.UserJID, role, unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) ListGroups(query string, limit int) ([]Group, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT jid, COALESCE(name,''), COALESCE(owner_jid,''), COALESCE(created_ts,0), updated_at FROM groups WHERE 1=1`
	var args []interface{}
	if strings.TrimSpace(query) != "" {
		needle := "%" + query + "%"
		q += ` AND (LOWER(name) LIKE LOWER(?) OR LOWER(jid) LIKE LOWER(?))`
		args = append(args, needle, needle)
	}
	q += ` ORDER BY COALESCE(created_ts,0) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var created, updated int64
		if err := rows.Scan(&g.JID, &g.Name, &g.OwnerJID, &created, &updated); err != nil {
			return nil, err
		}
		g.CreatedAt = fromUnix(created)
		g.UpdatedAt = fromUnix(updated)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (d *DB) SetAlias(jid, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("alias is required")
	}
	now := time.Now().UTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO contact_aliases(jid, alias, notes, updated_at)
		VALUES (?, ?, NULL, ?)
		ON CONFLICT(jid) DO UPDATE SET alias=excluded.alias, updated_at=excluded.updated_at
	`, jid, alias, now)
	return err
}

func (d *DB) RemoveAlias(jid string) error {
	_, err := d.sql.Exec(`DELETE FROM contact_aliases WHERE jid = ?`, jid)
	return err
}

// SetSystemName sets the system contact name for a contact (from macOS/iOS address book)
func (d *DB) SetSystemName(jid, systemName string) error {
	systemName = strings.TrimSpace(systemName)
	if systemName == "" {
		return fmt.Errorf("system_name is required")
	}
	now := time.Now().UTC().Unix()
	_, err := d.sql.Exec(`
		UPDATE contacts SET system_name = ?, updated_at = ? WHERE jid = ?
	`, systemName, now, jid)
	return err
}

// ClearSystemName removes the system contact name for a contact
func (d *DB) ClearSystemName(jid string) error {
	now := time.Now().UTC().Unix()
	_, err := d.sql.Exec(`
		UPDATE contacts SET system_name = NULL, updated_at = ? WHERE jid = ?
	`, now, jid)
	return err
}

// ClearAllSystemNames removes all system contact names
func (d *DB) ClearAllSystemNames() (int64, error) {
	now := time.Now().UTC().Unix()
	result, err := d.sql.Exec(`
		UPDATE contacts SET system_name = NULL, updated_at = ? WHERE system_name IS NOT NULL
	`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CountSystemNames returns the number of contacts with system names set
func (d *DB) CountSystemNames() (int64, error) {
	row := d.sql.QueryRow(`SELECT COUNT(1) FROM contacts WHERE system_name IS NOT NULL AND system_name != ''`)
	var n int64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (d *DB) AddTag(jid, tag string) error {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return fmt.Errorf("tag is required")
	}
	now := time.Now().UTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO contact_tags(jid, tag, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(jid, tag) DO UPDATE SET updated_at=excluded.updated_at
	`, jid, tag, now)
	return err
}

func (d *DB) RemoveTag(jid, tag string) error {
	_, err := d.sql.Exec(`DELETE FROM contact_tags WHERE jid = ? AND tag = ?`, jid, tag)
	return err
}

func (d *DB) HasFTS() bool { return d.ftsEnabled }

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
