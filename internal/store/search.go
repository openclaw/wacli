package store

import (
	"fmt"
	"strings"
	"time"
)

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
