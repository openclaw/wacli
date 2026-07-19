package store

import (
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store/storedb"
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
		return d.q.SetStarredDelete(storeCtx(), storedb.SetStarredDeleteParams{ChatJid: chatJID, MsgID: msgID})
	}
	starredAt := p.StarredAt
	if starredAt.IsZero() {
		starredAt = nowUTC()
	}
	return d.q.SetStarredUpsert(storeCtx(), storedb.SetStarredUpsertParams{
		ChatJid:   chatJID,
		MsgID:     msgID,
		SenderJid: nullString(p.SenderJID),
		FromMe:    boolToInt64(p.FromMe),
		StarredAt: unix(starredAt),
	})
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
		WHERE m.deleted_at IS NULL`
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
