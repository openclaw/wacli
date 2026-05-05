package store

import (
	"fmt"
	"strings"
	"time"
)

type UpsertMessageParams struct {
	ChatJID         string
	ChatName        string
	MsgID           string
	SenderJID       string
	SenderName      string
	Timestamp       time.Time
	FromMe          bool
	Text            string
	DisplayText     string
	IsForwarded     bool
	ForwardingScore uint32
	ReactionToID    string
	ReactionEmoji   string
	MediaType       string
	MediaCaption    string
	Filename        string
	MimeType        string
	DirectPath      string
	MediaKey        []byte
	FileSHA256      []byte
	FileEncSHA256   []byte
	FileLength      uint64
}

func messageSelectColumns(snippet string) string {
	return fmt.Sprintf(`m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), m.is_forwarded, m.forwarding_score, COALESCE(m.reaction_to_id,''), COALESCE(m.reaction_emoji,''), COALESCE(m.media_type,''), COALESCE(m.media_caption,''), COALESCE(m.filename,''), COALESCE(m.mime_type,''), COALESCE(m.direct_path,''), COALESCE(m.local_path,''), COALESCE(m.downloaded_at,0), CASE WHEN s.msg_id IS NULL THEN 0 ELSE 1 END, COALESCE(s.starred_at,0), %s`, snippetSQL(snippet))
}

func snippetSQL(snippet string) string {
	if strings.TrimSpace(snippet) == "" {
		return "''"
	}
	return snippet
}

func (d *DB) UpsertMessage(p UpsertMessageParams) error {
	_, err := d.sql.Exec(`
		INSERT INTO messages(
			chat_jid, chat_name, msg_id, sender_jid, sender_name, ts, from_me, text, display_text,
			is_forwarded, forwarding_score, reaction_to_id, reaction_emoji,
			media_type, media_caption, filename, mime_type, direct_path,
			media_key, file_sha256, file_enc_sha256, file_length
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			chat_name=COALESCE(NULLIF(excluded.chat_name,''), messages.chat_name),
			sender_jid=excluded.sender_jid,
			sender_name=COALESCE(NULLIF(excluded.sender_name,''), messages.sender_name),
			ts=excluded.ts,
			from_me=excluded.from_me,
			text=excluded.text,
			display_text=CASE WHEN excluded.display_text IS NOT NULL AND excluded.display_text != '' THEN excluded.display_text ELSE messages.display_text END,
			is_forwarded=excluded.is_forwarded,
			forwarding_score=excluded.forwarding_score,
			reaction_to_id=COALESCE(NULLIF(excluded.reaction_to_id,''), messages.reaction_to_id),
			reaction_emoji=COALESCE(NULLIF(excluded.reaction_emoji,''), messages.reaction_emoji),
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
		boolToInt(p.IsForwarded), int64(p.ForwardingScore), nullIfEmpty(p.ReactionToID), nullIfEmpty(p.ReactionEmoji),
		nullIfEmpty(p.MediaType), nullIfEmpty(p.MediaCaption), nullIfEmpty(p.Filename), nullIfEmpty(p.MimeType), nullIfEmpty(p.DirectPath),
		p.MediaKey, p.FileSHA256, p.FileEncSHA256, int64(p.FileLength),
	)
	return err
}

type ListMessagesParams struct {
	ChatJID   string
	ChatJIDs  []string
	SenderJID string
	Limit     int
	Before    *time.Time
	After     *time.Time
	FromMe    *bool
	Asc       bool
	Forwarded bool
	Starred   bool
}

func (d *DB) ListMessages(p ListMessagesParams) ([]Message, error) {
	if p.Limit <= 0 {
		p.Limit = 50
	}
	query := `
		SELECT ` + messageSelectColumns("") + `
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		LEFT JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
		WHERE 1=1`
	var args []interface{}
	query, args = appendStringFilter(query, args, "m.chat_jid", p.ChatJID, p.ChatJIDs)
	if p.After != nil {
		query += " AND m.ts > ?"
		args = append(args, unix(*p.After))
	}
	if p.Before != nil {
		query += " AND m.ts < ?"
		args = append(args, unix(*p.Before))
	}
	if strings.TrimSpace(p.SenderJID) != "" {
		query += " AND m.sender_jid = ?"
		args = append(args, strings.TrimSpace(p.SenderJID))
	}
	if p.FromMe != nil {
		query += " AND m.from_me = ?"
		args = append(args, boolToInt(*p.FromMe))
	}
	if p.Forwarded {
		query += " AND m.is_forwarded = 1"
	}
	if p.Starred {
		query += " AND s.msg_id IS NOT NULL"
	}
	if p.Asc {
		query += " ORDER BY m.ts ASC, m.rowid ASC LIMIT ?"
	} else {
		query += " ORDER BY m.ts DESC, m.rowid DESC LIMIT ?"
	}
	args = append(args, p.Limit)
	return d.scanMessages(query, args...)
}

func appendStringFilter(query string, args []interface{}, column, value string, values []string) (string, []interface{}) {
	filterValues := uniqueNonEmptyStrings(append([]string{value}, values...))
	switch len(filterValues) {
	case 0:
		return query, args
	case 1:
		query += " AND " + column + " = ?"
		args = append(args, filterValues[0])
		return query, args
	default:
		query += " AND " + column + " IN (" + strings.TrimRight(strings.Repeat("?,", len(filterValues)), ",") + ")"
		for _, v := range filterValues {
			args = append(args, v)
		}
		return query, args
	}
}

func uniqueNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (d *DB) GetMessage(chatJID, msgID string) (Message, error) {
	row := d.sql.QueryRow(`
		SELECT `+messageSelectColumns("")+`
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		LEFT JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
		WHERE m.chat_jid = ? AND m.msg_id = ?
	`, chatJID, msgID)
	var m Message
	var ts int64
	var fromMe int
	var forwarded int
	var forwardingScore int64
	var downloadedAt int64
	var starred int
	var starredAt int64
	if err := row.Scan(&m.rowID, &m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &m.SenderName, &ts, &fromMe, &m.Text, &m.DisplayText, &forwarded, &forwardingScore, &m.ReactionToID, &m.ReactionEmoji, &m.MediaType, &m.MediaCaption, &m.Filename, &m.MimeType, &m.DirectPath, &m.LocalPath, &downloadedAt, &starred, &starredAt, &m.Snippet); err != nil {
		return Message{}, err
	}
	m.Timestamp = fromUnix(ts)
	m.FromMe = fromMe != 0
	m.IsForwarded = forwarded != 0
	m.ForwardingScore = uint32(forwardingScore)
	m.DownloadedAt = fromUnix(downloadedAt)
	m.Starred = starred != 0
	m.StarredAt = fromUnix(starredAt)
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
		ORDER BY m.ts ASC, m.rowid ASC
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
			SELECT `+messageSelectColumns("")+`
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		LEFT JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
		WHERE m.chat_jid = ? AND (m.ts < ? OR (m.ts = ? AND m.rowid < ?))
		ORDER BY m.ts DESC, m.rowid DESC
		LIMIT ?
	`, chatJID, unix(target.Timestamp), unix(target.Timestamp), target.rowID, before)
	if err != nil {
		return nil, err
	}

	afterRows, err := d.scanMessages(`
			SELECT `+messageSelectColumns("")+`
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		LEFT JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
		WHERE m.chat_jid = ? AND (m.ts > ? OR (m.ts = ? AND m.rowid > ?))
		ORDER BY m.ts ASC, m.rowid ASC
		LIMIT ?
	`, chatJID, unix(target.Timestamp), unix(target.Timestamp), target.rowID, after)
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
		var forwarded int
		var forwardingScore int64
		var downloadedAt int64
		var starred int
		var starredAt int64
		if err := rows.Scan(&m.rowID, &m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &m.SenderName, &ts, &fromMe, &m.Text, &m.DisplayText, &forwarded, &forwardingScore, &m.ReactionToID, &m.ReactionEmoji, &m.MediaType, &m.MediaCaption, &m.Filename, &m.MimeType, &m.DirectPath, &m.LocalPath, &downloadedAt, &starred, &starredAt, &m.Snippet); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.FromMe = fromMe != 0
		m.IsForwarded = forwarded != 0
		m.ForwardingScore = uint32(forwardingScore)
		m.DownloadedAt = fromUnix(downloadedAt)
		m.Starred = starred != 0
		m.StarredAt = fromUnix(starredAt)
		out = append(out, m)
	}
	return out, rows.Err()
}
