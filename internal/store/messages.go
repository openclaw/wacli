package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store/storedb"
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
	Buttons         []Button
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
	Edited          bool
	Revoked         bool
	DeletedForMe    bool
}

func messageSelectColumns(snippet string) string {
	return fmt.Sprintf(`m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), m.is_forwarded, m.forwarding_score, COALESCE(m.reaction_to_id,''), COALESCE(m.reaction_emoji,''), COALESCE(m.media_type,''), COALESCE(m.media_caption,''), COALESCE(m.filename,''), COALESCE(m.mime_type,''), COALESCE(m.direct_path,''), COALESCE(m.local_path,''), COALESCE(m.downloaded_at,0), CASE WHEN s.msg_id IS NULL THEN 0 ELSE 1 END, COALESCE(s.starred_at,0), m.revoked, m.deleted_for_me, COALESCE(m.buttons,''), %s`, snippetSQL(snippet))
}

func snippetSQL(snippet string) string {
	if strings.TrimSpace(snippet) == "" {
		return "''"
	}
	return snippet
}

func (d *DB) UpsertMessage(p UpsertMessageParams) error {
	if p.Revoked || p.DeletedForMe {
		p.Text = ""
		p.Buttons = nil
		if p.DeletedForMe {
			p.DisplayText = DeletedForMeMessageDisplayText
		} else {
			p.DisplayText = DeletedMessageDisplayText
		}
		p.MediaType = ""
		p.MediaCaption = ""
		p.Filename = ""
		p.MimeType = ""
		p.DirectPath = ""
		p.MediaKey = nil
		p.FileSHA256 = nil
		p.FileEncSHA256 = nil
		p.FileLength = 0
	}
	var buttonsJSON interface{}
	if len(p.Buttons) > 0 {
		if b, err := json.Marshal(p.Buttons); err == nil {
			buttonsJSON = string(b)
		}
	}
	_, err := d.sql.Exec(`
		INSERT INTO messages(
			chat_jid, chat_name, msg_id, sender_jid, sender_name, ts, from_me, text, display_text,
			is_forwarded, forwarding_score, reaction_to_id, reaction_emoji,
			media_type, media_caption, filename, mime_type, direct_path,
			media_key, file_sha256, file_enc_sha256, file_length, revoked, deleted_for_me, edited, buttons
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			chat_name=COALESCE(NULLIF(excluded.chat_name,''), messages.chat_name),
			sender_jid=CASE WHEN (excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0)) AND messages.revoked = 0 AND messages.deleted_for_me = 0 AND excluded.revoked = 0 AND excluded.deleted_for_me = 0 THEN messages.sender_jid ELSE excluded.sender_jid END,
			sender_name=CASE WHEN (excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0)) AND messages.revoked = 0 AND messages.deleted_for_me = 0 AND excluded.revoked = 0 AND excluded.deleted_for_me = 0 THEN messages.sender_name ELSE COALESCE(NULLIF(excluded.sender_name,''), messages.sender_name) END,
			ts=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 THEN messages.ts WHEN excluded.edited != 0 THEN messages.ts WHEN messages.edited != 0 AND excluded.edited = 0 THEN excluded.ts WHEN excluded.ts < messages.ts AND excluded.revoked = 0 AND excluded.deleted_for_me = 0 THEN messages.ts ELSE excluded.ts END,
			from_me=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR ((excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0)) AND excluded.revoked = 0 AND excluded.deleted_for_me = 0) THEN messages.from_me ELSE excluded.from_me END,
			text=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.text ELSE excluded.text END,
			display_text=CASE WHEN messages.deleted_for_me != 0 OR excluded.deleted_for_me != 0 THEN ? WHEN messages.revoked != 0 OR excluded.revoked != 0 THEN ? WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.display_text WHEN excluded.display_text IS NOT NULL AND excluded.display_text != '' THEN excluded.display_text ELSE messages.display_text END,
			is_forwarded=CASE WHEN (excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0)) AND messages.revoked = 0 AND messages.deleted_for_me = 0 AND excluded.revoked = 0 AND excluded.deleted_for_me = 0 THEN messages.is_forwarded ELSE excluded.is_forwarded END,
			forwarding_score=CASE WHEN (excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0)) AND messages.revoked = 0 AND messages.deleted_for_me = 0 AND excluded.revoked = 0 AND excluded.deleted_for_me = 0 THEN messages.forwarding_score ELSE excluded.forwarding_score END,
			reaction_to_id=CASE WHEN (excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0)) AND messages.revoked = 0 AND messages.deleted_for_me = 0 AND excluded.revoked = 0 AND excluded.deleted_for_me = 0 THEN messages.reaction_to_id ELSE COALESCE(NULLIF(excluded.reaction_to_id,''), messages.reaction_to_id) END,
			reaction_emoji=CASE WHEN (excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0)) AND messages.revoked = 0 AND messages.deleted_for_me = 0 AND excluded.revoked = 0 AND excluded.deleted_for_me = 0 THEN messages.reaction_emoji ELSE COALESCE(NULLIF(excluded.reaction_emoji,''), messages.reaction_emoji) END,
			media_type=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.media_type ELSE excluded.media_type END,
			media_caption=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.media_caption ELSE excluded.media_caption END,
			filename=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.filename ELSE COALESCE(NULLIF(excluded.filename,''), messages.filename) END,
			mime_type=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.mime_type ELSE COALESCE(NULLIF(excluded.mime_type,''), messages.mime_type) END,
			direct_path=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.direct_path ELSE COALESCE(NULLIF(excluded.direct_path,''), messages.direct_path) END,
			media_key=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.media_key WHEN excluded.media_key IS NOT NULL AND length(excluded.media_key)>0 THEN excluded.media_key ELSE messages.media_key END,
			file_sha256=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.file_sha256 WHEN excluded.file_sha256 IS NOT NULL AND length(excluded.file_sha256)>0 THEN excluded.file_sha256 ELSE messages.file_sha256 END,
			file_enc_sha256=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.file_enc_sha256 WHEN excluded.file_enc_sha256 IS NOT NULL AND length(excluded.file_enc_sha256)>0 THEN excluded.file_enc_sha256 ELSE messages.file_enc_sha256 END,
			file_length=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.file_length WHEN excluded.file_length>0 THEN excluded.file_length ELSE messages.file_length END,
			local_path=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE messages.local_path END,
			downloaded_at=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE messages.downloaded_at END,
			revoked=CASE WHEN excluded.revoked != 0 THEN 1 ELSE messages.revoked END,
			deleted_for_me=CASE WHEN excluded.deleted_for_me != 0 THEN 1 ELSE messages.deleted_for_me END,
			edited=CASE WHEN excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN 0 WHEN excluded.edited != 0 THEN 1 WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.edited ELSE 0 END,
			buttons=CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.ts < messages.ts OR (excluded.ts = messages.ts AND messages.edited != 0 AND excluded.edited = 0) THEN messages.buttons ELSE excluded.buttons END
	`, p.ChatJID, nullIfEmpty(p.ChatName), p.MsgID, nullIfEmpty(p.SenderJID), nullIfEmpty(p.SenderName), unix(p.Timestamp), boolToInt(p.FromMe), nullIfEmpty(p.Text), nullIfEmpty(p.DisplayText),
		boolToInt(p.IsForwarded), int64(p.ForwardingScore), nullIfEmpty(p.ReactionToID), nullIfEmpty(p.ReactionEmoji),
		nullIfEmpty(p.MediaType), nullIfEmpty(p.MediaCaption), nullIfEmpty(p.Filename), nullIfEmpty(p.MimeType), nullIfEmpty(p.DirectPath),
		p.MediaKey, p.FileSHA256, p.FileEncSHA256, int64(p.FileLength), boolToInt(p.Revoked), boolToInt(p.DeletedForMe), boolToInt(p.Edited), buttonsJSON,
		DeletedForMeMessageDisplayText, DeletedMessageDisplayText,
	)
	return err
}

func (d *DB) MarkMessageRevoked(chatJID, msgID string) error {
	n, err := d.q.MarkMessageRevoked(storeCtx(), storedb.MarkMessageRevokedParams{
		DisplayText: sql.NullString{String: DeletedMessageDisplayText, Valid: true},
		ChatJid:     strings.TrimSpace(chatJID),
		MsgID:       strings.TrimSpace(msgID),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *DB) MarkMessageDeletedForMe(chatJID, msgID, senderJID string, fromMe bool, deletedAt time.Time) error {
	chatJID = strings.TrimSpace(chatJID)
	msgID = strings.TrimSpace(msgID)
	if chatJID == "" {
		return fmt.Errorf("chat JID is required")
	}
	if msgID == "" {
		return fmt.Errorf("message ID is required")
	}
	if deletedAt.IsZero() {
		deletedAt = nowUTC()
	}
	n, err := d.q.MarkMessageDeletedForMe(storeCtx(), storedb.MarkMessageDeletedForMeParams{
		DisplayText: sql.NullString{String: DeletedForMeMessageDisplayText, Valid: true},
		ChatJid:     chatJID,
		MsgID:       msgID,
	})
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return d.UpsertMessage(UpsertMessageParams{
		ChatJID:      chatJID,
		MsgID:        msgID,
		SenderJID:    senderJID,
		Timestamp:    deletedAt,
		FromMe:       fromMe,
		DeletedForMe: true,
	})
}

func (d *DB) UpdateMessageText(chatJID, msgID, text string) error {
	n, err := d.q.UpdateMessageText(storeCtx(), storedb.UpdateMessageTextParams{
		Text:        nullString(text),
		DisplayText: nullString(text),
		ChatJid:     strings.TrimSpace(chatJID),
		MsgID:       strings.TrimSpace(msgID),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
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
		WHERE m.revoked = 0 AND m.deleted_for_me = 0`
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
	row, err := d.q.GetMessage(storeCtx(), storedb.GetMessageParams{ChatJid: chatJID, MsgID: msgID})
	if err != nil {
		return Message{}, err
	}
	return messageFromGetRow(row), nil
}

func (d *DB) CountMessages() (int64, error) {
	return d.q.CountMessages(storeCtx())
}

func (d *DB) GetOldestMessageInfo(chatJID string) (MessageInfo, error) {
	chatJID = strings.TrimSpace(chatJID)
	if chatJID == "" {
		return MessageInfo{}, fmt.Errorf("chat JID is required")
	}
	row, err := d.q.GetOldestMessageInfo(storeCtx(), chatJID)
	if err != nil {
		return MessageInfo{}, err
	}
	return messageInfoFromOldestRow(row), nil
}

func (d *DB) GetLatestMessageInfo(chatJID string) (MessageInfo, error) {
	chatJID = strings.TrimSpace(chatJID)
	if chatJID == "" {
		return MessageInfo{}, fmt.Errorf("chat JID is required")
	}
	row, err := d.q.GetLatestMessageInfo(storeCtx(), chatJID)
	if err != nil {
		return MessageInfo{}, err
	}
	return messageInfoFromLatestRow(row), nil
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

	beforeRows, err := d.q.MessageContextBefore(storeCtx(), storedb.MessageContextBeforeParams{
		ChatJid: chatJID,
		Ts:      unix(target.Timestamp),
		Ts_2:    unix(target.Timestamp),
		Rowid:   target.rowID,
		Limit:   int64(before),
	})
	if err != nil {
		return nil, err
	}

	afterRows, err := d.q.MessageContextAfter(storeCtx(), storedb.MessageContextAfterParams{
		ChatJid: chatJID,
		Ts:      unix(target.Timestamp),
		Ts_2:    unix(target.Timestamp),
		Rowid:   target.rowID,
		Limit:   int64(after),
	})
	if err != nil {
		return nil, err
	}
	beforeMessages := make([]Message, 0, len(beforeRows))
	for _, row := range beforeRows {
		beforeMessages = append(beforeMessages, messageFromBeforeRow(row))
	}
	afterMessages := make([]Message, 0, len(afterRows))
	for _, row := range afterRows {
		afterMessages = append(afterMessages, messageFromAfterRow(row))
	}

	// Reverse before rows back to chronological order.
	for i, j := 0, len(beforeMessages)-1; i < j; i, j = i+1, j-1 {
		beforeMessages[i], beforeMessages[j] = beforeMessages[j], beforeMessages[i]
	}

	out := make([]Message, 0, len(beforeMessages)+1+len(afterMessages))
	out = append(out, beforeMessages...)
	out = append(out, target)
	out = append(out, afterMessages...)
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
		var revoked int
		var deletedForMe int
		var buttonsJSON string
		if err := rows.Scan(&m.rowID, &m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &m.SenderName, &ts, &fromMe, &m.Text, &m.DisplayText, &forwarded, &forwardingScore, &m.ReactionToID, &m.ReactionEmoji, &m.MediaType, &m.MediaCaption, &m.Filename, &m.MimeType, &m.DirectPath, &m.LocalPath, &downloadedAt, &starred, &starredAt, &revoked, &deletedForMe, &buttonsJSON, &m.Snippet); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		m.FromMe = fromMe != 0
		m.IsForwarded = forwarded != 0
		m.ForwardingScore = uint32(forwardingScore)
		m.DownloadedAt = fromUnix(downloadedAt)
		m.Starred = starred != 0
		m.StarredAt = fromUnix(starredAt)
		m.Revoked = revoked != 0
		m.DeletedForMe = deletedForMe != 0
		if buttonsJSON != "" {
			_ = json.Unmarshal([]byte(buttonsJSON), &m.Buttons)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func messageFromGetRow(row storedb.GetMessageRow) Message {
	return messageFromScalars(
		row.Rowid, row.ChatJid, row.Name, row.MsgID, row.SenderJid, row.SenderName,
		row.Ts, row.FromMe, row.Text, row.DisplayText, row.IsForwarded,
		row.ForwardingScore, row.ReactionToID, row.ReactionEmoji, row.MediaType,
		row.MediaCaption, row.Filename, row.MimeType, row.DirectPath, row.LocalPath,
		row.DownloadedAt, row.Column22, row.StarredAt, row.Revoked, row.DeletedForMe,
		row.Buttons, row.Column27,
	)
}

func messageFromBeforeRow(row storedb.MessageContextBeforeRow) Message {
	return messageFromScalars(
		row.Rowid, row.ChatJid, row.Name, row.MsgID, row.SenderJid, row.SenderName,
		row.Ts, row.FromMe, row.Text, row.DisplayText, row.IsForwarded,
		row.ForwardingScore, row.ReactionToID, row.ReactionEmoji, row.MediaType,
		row.MediaCaption, row.Filename, row.MimeType, row.DirectPath, row.LocalPath,
		row.DownloadedAt, row.Column22, row.StarredAt, row.Revoked, row.DeletedForMe,
		row.Buttons, row.Column27,
	)
}

func messageFromAfterRow(row storedb.MessageContextAfterRow) Message {
	return messageFromScalars(
		row.Rowid, row.ChatJid, row.Name, row.MsgID, row.SenderJid, row.SenderName,
		row.Ts, row.FromMe, row.Text, row.DisplayText, row.IsForwarded,
		row.ForwardingScore, row.ReactionToID, row.ReactionEmoji, row.MediaType,
		row.MediaCaption, row.Filename, row.MimeType, row.DirectPath, row.LocalPath,
		row.DownloadedAt, row.Column22, row.StarredAt, row.Revoked, row.DeletedForMe,
		row.Buttons, row.Column27,
	)
}

func messageFromScalars(rowID int64, chatJID, chatName, msgID, senderJID, senderName string, ts, fromMe int64, text, displayText string, forwarded, forwardingScore int64, reactionToID, reactionEmoji, mediaType, mediaCaption, filename, mimeType, directPath, localPath string, downloadedAt, starred, starredAt, revoked, deletedForMe int64, buttonsJSON, snippet string) Message {
	m := Message{
		rowID:           rowID,
		ChatJID:         chatJID,
		ChatName:        chatName,
		MsgID:           msgID,
		SenderJID:       senderJID,
		SenderName:      senderName,
		Timestamp:       fromUnix(ts),
		FromMe:          fromMe != 0,
		Text:            text,
		DisplayText:     displayText,
		IsForwarded:     forwarded != 0,
		ForwardingScore: uint32(forwardingScore),
		ReactionToID:    reactionToID,
		ReactionEmoji:   reactionEmoji,
		MediaType:       mediaType,
		MediaCaption:    mediaCaption,
		Filename:        filename,
		MimeType:        mimeType,
		DirectPath:      directPath,
		LocalPath:       localPath,
		DownloadedAt:    fromUnix(downloadedAt),
		Starred:         starred != 0,
		StarredAt:       fromUnix(starredAt),
		Revoked:         revoked != 0,
		DeletedForMe:    deletedForMe != 0,
		Snippet:         snippet,
	}
	if buttonsJSON != "" {
		_ = json.Unmarshal([]byte(buttonsJSON), &m.Buttons)
	}
	return m
}

func messageInfoFromOldestRow(row storedb.GetOldestMessageInfoRow) MessageInfo {
	return MessageInfo{
		ChatJID:    row.ChatJid,
		MsgID:      row.MsgID,
		Timestamp:  fromUnix(row.Ts),
		FromMe:     row.FromMe != 0,
		SenderJID:  row.SenderJid,
		SenderName: row.SenderName,
	}
}

func messageInfoFromLatestRow(row storedb.GetLatestMessageInfoRow) MessageInfo {
	return MessageInfo{
		ChatJID:    row.ChatJid,
		MsgID:      row.MsgID,
		Timestamp:  fromUnix(row.Ts),
		FromMe:     row.FromMe != 0,
		SenderJID:  row.SenderJid,
		SenderName: row.SenderName,
	}
}
