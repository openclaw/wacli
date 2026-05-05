package store

import (
	"fmt"
	"strings"
	"time"
)

type SetStarredParams struct {
	ChatJID   string
	MsgID     string
	SenderJID string
	FromMe    bool
	Starred   bool
	StarredAt time.Time
}

type ListStarredMessagesParams struct {
	ChatJID  string
	ChatJIDs []string
	Limit    int
	Before   *time.Time
	After    *time.Time
	Asc      bool
}

func (d *DB) SetStarred(p SetStarredParams) error {
	chatJID := strings.TrimSpace(p.ChatJID)
	msgID := strings.TrimSpace(p.MsgID)
	if chatJID == "" {
		return fmt.Errorf("chat JID is required")
	}
	if msgID == "" {
		return fmt.Errorf("message ID is required")
	}
	if !p.Starred {
		_, err := d.sql.Exec(`DELETE FROM starred WHERE chat_jid = ? AND msg_id = ?`, chatJID, msgID)
		return err
	}
	starredAt := p.StarredAt
	if starredAt.IsZero() {
		starredAt = nowUTC()
	}
	_, err := d.sql.Exec(`
		INSERT INTO starred(chat_jid, msg_id, sender_jid, from_me, starred_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			sender_jid=COALESCE(NULLIF(excluded.sender_jid,''), starred.sender_jid),
			from_me=excluded.from_me,
			starred_at=excluded.starred_at
	`, chatJID, msgID, nullIfEmpty(p.SenderJID), boolToInt(p.FromMe), unix(starredAt))
	return err
}

func (d *DB) ListStarredMessages(p ListStarredMessagesParams) ([]Message, error) {
	if p.Limit <= 0 {
		p.Limit = 50
	}
	query := `
		SELECT ` + messageSelectColumns("") + `
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
		WHERE 1=1`
	var args []interface{}
	query, args = appendStringFilter(query, args, "m.chat_jid", p.ChatJID, p.ChatJIDs)
	if p.After != nil {
		query += " AND s.starred_at > ?"
		args = append(args, unix(*p.After))
	}
	if p.Before != nil {
		query += " AND s.starred_at < ?"
		args = append(args, unix(*p.Before))
	}
	if p.Asc {
		query += " ORDER BY s.starred_at ASC, m.rowid ASC LIMIT ?"
	} else {
		query += " ORDER BY s.starred_at DESC, m.rowid DESC LIMIT ?"
	}
	args = append(args, p.Limit)
	return d.scanMessages(query, args...)
}
