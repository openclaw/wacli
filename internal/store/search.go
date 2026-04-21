package store

import (
	"fmt"
	"strings"
	"time"
)

type SearchMessagesParams struct {
	Query    string
	ChatJID  string
	From     string
	Limit    int
	Before   *time.Time
	After    *time.Time
	HasMedia bool
	Type     string
}

func (d *DB) SearchMessages(p SearchMessagesParams) ([]Message, error) {
	if strings.TrimSpace(p.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	msgType := normalizedMessageType(p.Type)
	if msgType != "" && !validSearchMessageType(msgType) {
		return nil, fmt.Errorf("unsupported message type %q", p.Type)
	}
	if p.HasMedia && msgType == "text" {
		return nil, fmt.Errorf("cannot combine has-media with type=text")
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}

	if d.ftsEnabled {
		return d.searchFTS(p)
	}
	return d.searchLIKE(p)
}

// escapeLIKE escapes SQL LIKE wildcard characters (%, _) and the escape
// character itself so that user input is treated as a literal string (#56).
func escapeLIKE(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func likeContains(s string) string {
	return "%" + escapeLIKE(s) + "%"
}

func (d *DB) searchLIKE(p SearchMessagesParams) ([]Message, error) {
	query := `
		SELECT m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''), ''
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
	WHERE (LOWER(m.text) LIKE LOWER(?) ESCAPE '\' OR LOWER(m.display_text) LIKE LOWER(?) ESCAPE '\' OR LOWER(m.media_caption) LIKE LOWER(?) ESCAPE '\' OR LOWER(m.filename) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(m.chat_name,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(m.sender_name,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(COALESCE(c.name,'')) LIKE LOWER(?) ESCAPE '\')`
	// Escape wildcards before wrapping in % so user input is literal (#56).
	needle := likeContains(p.Query)
	args := []interface{}{needle, needle, needle, needle, needle, needle, needle}
	query, args = applyMessageFilters(query, args, p)
	query += " ORDER BY m.ts DESC, m.rowid DESC LIMIT ?"
	args = append(args, p.Limit)
	return d.scanMessages(query, args...)
}

// sanitizeFTSQuery converts a raw user query into a safe FTS5 expression by
// quoting each whitespace-delimited token individually. This prevents FTS5
// query-syntax injection (AND/OR/NOT/NEAR/column filters) while preserving
// intuitive multi-word search: "hello world" matches messages containing both
// words (implicit AND), not necessarily as an exact phrase.
func sanitizeFTSQuery(q string) string {
	tokens := strings.Fields(q)
	if len(tokens) == 0 {
		return `""`
	}
	quoted := make([]string, len(tokens))
	for i, tok := range tokens {
		// Escape embedded double-quotes by doubling them (FTS5 convention).
		quoted[i] = `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " ")
}

func (d *DB) searchFTS(p SearchMessagesParams) ([]Message, error) {
	query := `
		SELECT m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.media_type,''),
		       snippet(messages_fts, 0, '[', ']', '…', 12)
		FROM messages_fts
		JOIN messages m ON messages_fts.rowid = m.rowid
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE messages_fts MATCH ?`
	// Sanitize to prevent FTS5 query-syntax injection (#57).
	// Each token is individually quoted so multi-word queries still work
	// as implicit AND (both words present, any order).
	args := []interface{}{sanitizeFTSQuery(p.Query)}
	query, args = applyMessageFilters(query, args, p)
	query += " ORDER BY bm25(messages_fts), m.rowid DESC LIMIT ?"
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
	if p.HasMedia {
		query += " AND COALESCE(m.media_type,'') != ''"
	}
	if msgType := normalizedMessageType(p.Type); msgType != "" {
		if msgType == "text" {
			query += " AND COALESCE(m.media_type,'') = ''"
		} else {
			query += " AND LOWER(COALESCE(m.media_type,'')) = ?"
			args = append(args, msgType)
		}
	}
	return query, args
}

func normalizedMessageType(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func validSearchMessageType(s string) bool {
	switch s {
	case "text", "image", "video", "audio", "document":
		return true
	default:
		return false
	}
}
