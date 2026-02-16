package store

import (
	"fmt"
	"strings"
	"time"
)

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
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''), ''
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
	return d.scanMessages(query, args...)
}

func (d *DB) GetMessage(chatJID, msgID string) (Message, error) {
	row := d.sql.QueryRow(`
		SELECT m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''), ''
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ? AND m.msg_id = ?
	`, chatJID, msgID)
	var m Message
	var ts int64
	var fromMe int
	if err := row.Scan(&m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &ts, &fromMe, &m.Text, &m.DisplayText, &m.MediaType, &m.Snippet); err != nil {
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

	beforeRows, err := d.scanMessages(`
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

	afterRows, err := d.scanMessages(`
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

	// Reverse before rows back to chronological order.
	for i, j := 0, len(beforeRows)-1; i < j; i, j = i+1, j-1 {
		beforeRows[i], beforeRows[j] = beforeRows[j], beforeRows[i]
	}

	out := make([]Message, 0, len(beforeRows)+1+len(afterRows))
	out = append(out, beforeRows...)
	out = append(out, target)
	out = append(out, afterRows...)
	return out, nil
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
