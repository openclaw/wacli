package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store/storedb"
)

var ErrMessageNotTombstoned = errors.New("message is not tombstoned")

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
	QuotedMsgID     string
	QuotedSenderJID string
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
	DeletedAt       time.Time
	DeletionReason  string
}

func messageSelectColumns(snippet string) string {
	return fmt.Sprintf(`m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), COALESCE(m.quoted_msg_id,''), COALESCE(m.quoted_sender_jid,''), m.is_forwarded, m.forwarding_score, COALESCE(m.reaction_to_id,''), COALESCE(m.reaction_emoji,''), COALESCE(m.media_type,''), COALESCE(m.media_caption,''), COALESCE(m.filename,''), COALESCE(m.mime_type,''), COALESCE(m.direct_path,''), COALESCE(m.local_path,''), COALESCE(m.downloaded_at,0), CASE WHEN s.msg_id IS NULL THEN 0 ELSE 1 END, COALESCE(s.starred_at,0), m.revoked, m.deleted_for_me, COALESCE(m.deleted_at,0), COALESCE(m.deletion_reason,''), COALESCE(m.payload_purged_at,0), COALESCE(m.buttons,''), %s`, snippetSQL(snippet))
}

func snippetSQL(snippet string) string {
	if strings.TrimSpace(snippet) == "" {
		return "''"
	}
	return snippet
}

func (d *DB) UpsertMessage(p UpsertMessageParams) error {
	var deletedAt sql.NullInt64
	if p.Revoked || p.DeletedForMe || !p.DeletedAt.IsZero() {
		if p.DeletedAt.IsZero() {
			p.DeletedAt = p.Timestamp
		}
		if p.DeletedAt.IsZero() {
			p.DeletedAt = nowUTC()
		}
		deletedAt = sql.NullInt64{Int64: unix(p.DeletedAt), Valid: true}
		if strings.TrimSpace(p.DeletionReason) == "" {
			switch {
			case p.DeletedForMe:
				p.DeletionReason = MessageDeletionReasonWhatsAppDeleteForMe
			case p.Revoked:
				p.DeletionReason = MessageDeletionReasonWhatsAppRevoke
			default:
				p.DeletionReason = MessageDeletionReasonExplicit
			}
		}
	}
	var buttonsJSON sql.NullString
	if len(p.Buttons) > 0 {
		if b, err := json.Marshal(p.Buttons); err == nil {
			buttonsJSON = sql.NullString{String: string(b), Valid: true}
		}
	}
	editedTS := int64(0)
	if p.Edited {
		editedTS = unix(p.Timestamp)
	}
	return d.q.UpsertMessage(storeCtx(), storedb.UpsertMessageParams{
		ChatJid:         p.ChatJID,
		ChatName:        nullString(p.ChatName),
		MsgID:           p.MsgID,
		SenderJid:       nullString(p.SenderJID),
		SenderName:      nullString(p.SenderName),
		Ts:              unix(p.Timestamp),
		FromMe:          boolToInt64(p.FromMe),
		Text:            nullString(p.Text),
		DisplayText:     nullString(p.DisplayText),
		QuotedMsgID:     nullString(p.QuotedMsgID),
		QuotedSenderJid: nullString(p.QuotedSenderJID),
		IsForwarded:     boolToInt64(p.IsForwarded),
		ForwardingScore: int64(p.ForwardingScore),
		ReactionToID:    nullString(p.ReactionToID),
		ReactionEmoji:   nullString(p.ReactionEmoji),
		MediaType:       nullString(p.MediaType),
		MediaCaption:    nullString(p.MediaCaption),
		Filename:        nullString(p.Filename),
		MimeType:        nullString(p.MimeType),
		DirectPath:      nullString(p.DirectPath),
		MediaKey:        p.MediaKey,
		FileSha256:      p.FileSHA256,
		FileEncSha256:   p.FileEncSHA256,
		FileLength:      sqlNullInt64(int64(p.FileLength)),
		Revoked:         boolToInt64(p.Revoked),
		DeletedForMe:    boolToInt64(p.DeletedForMe),
		DeletedAt:       deletedAt,
		DeletionReason:  nullString(p.DeletionReason),
		Edited:          boolToInt64(p.Edited),
		EditedTs:        editedTS,
		Buttons:         buttonsJSON,
		ChatJid_2:       strings.TrimSpace(p.ChatJID),
		MsgID_2:         strings.TrimSpace(p.MsgID),
	})
}

func (d *DB) MarkMessageRevoked(chatJID, msgID string) error {
	n, err := d.q.MarkMessageRevoked(storeCtx(), storedb.MarkMessageRevokedParams{
		DeletedAt:      sql.NullInt64{Int64: nowUTC().Unix(), Valid: true},
		DeletionReason: nullString(MessageDeletionReasonWhatsAppRevoke),
		ChatJid:        strings.TrimSpace(chatJID),
		MsgID:          strings.TrimSpace(msgID),
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
		DeletedAt:      sql.NullInt64{Int64: unix(deletedAt), Valid: true},
		DeletionReason: nullString(MessageDeletionReasonWhatsAppDeleteForMe),
		ChatJid:        chatJID,
		MsgID:          msgID,
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
		DeletedAt:    deletedAt,
	})
}

func (d *DB) MarkMessageDeletedForMePreserveMedia(chatJID, msgID string) error {
	chatJID = strings.TrimSpace(chatJID)
	msgID = strings.TrimSpace(msgID)
	if chatJID == "" {
		return fmt.Errorf("chat JID is required")
	}
	if msgID == "" {
		return fmt.Errorf("message ID is required")
	}
	n, err := d.q.MarkMessageDeletedForMePreserveMedia(storeCtx(), storedb.MarkMessageDeletedForMePreserveMediaParams{
		DeletedAt:      sql.NullInt64{Int64: nowUTC().Unix(), Valid: true},
		DeletionReason: nullString(MessageDeletionReasonWhatsAppDeleteForMe),
		ChatJid:        chatJID,
		MsgID:          msgID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *DB) ClearMessageLocalMedia(chatJID, msgID string) error {
	res, err := d.sql.Exec(`UPDATE messages SET local_path = NULL, downloaded_at = NULL WHERE chat_jid = ? AND msg_id = ?`, strings.TrimSpace(chatJID), strings.TrimSpace(msgID))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
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

func (d *DB) PurgeMessage(chatJID, msgID string) error {
	chatJID = strings.TrimSpace(chatJID)
	msgID = strings.TrimSpace(msgID)
	if chatJID == "" {
		return fmt.Errorf("chat JID is required")
	}
	if msgID == "" {
		return fmt.Errorf("message ID is required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deletedAt sql.NullInt64
	var deletionReason sql.NullString
	if err := tx.QueryRow(`SELECT deleted_at, deletion_reason FROM messages WHERE chat_jid = ? AND msg_id = ?`, chatJID, msgID).Scan(&deletedAt, &deletionReason); err != nil {
		return err
	}
	if !deletedAt.Valid {
		return ErrMessageNotTombstoned
	}
	if strings.TrimSpace(deletionReason.String) == "" {
		deletionReason = nullString(MessageDeletionReasonExplicit)
	}
	purgedAt := nowUTC().Unix()
	if _, err := tx.Exec(`
		INSERT INTO message_payload_purges(chat_jid, msg_id, purged_at, deleted_at, deletion_reason)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			purged_at = min(message_payload_purges.purged_at, excluded.purged_at),
			deleted_at = min(message_payload_purges.deleted_at, excluded.deleted_at),
			deletion_reason = COALESCE(NULLIF(message_payload_purges.deletion_reason, ''), excluded.deletion_reason)
	`, chatJID, msgID, purgedAt, deletedAt.Int64, deletionReason.String); err != nil {
		return err
	}
	for _, stmt := range []string{
		`DELETE FROM poll_votes WHERE chat_jid = ? AND (poll_msg_id = ? OR vote_msg_id = ?)`,
		`DELETE FROM polls WHERE chat_jid = ? AND msg_id = ?`,
		`DELETE FROM starred WHERE chat_jid = ? AND msg_id = ?`,
	} {
		args := []any{chatJID, msgID}
		if strings.Contains(stmt, "vote_msg_id") {
			args = append(args, msgID)
		}
		if _, err := tx.Exec(stmt, args...); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
		UPDATE messages SET
			chat_name = NULL,
			sender_jid = NULL,
			sender_name = NULL,
			text = NULL,
			display_text = NULL,
			quoted_msg_id = NULL,
			quoted_sender_jid = NULL,
			is_forwarded = 0,
			forwarding_score = 0,
			reaction_to_id = NULL,
			reaction_emoji = NULL,
			media_type = NULL,
			media_caption = NULL,
			filename = NULL,
			mime_type = NULL,
			direct_path = NULL,
			media_key = NULL,
			file_sha256 = NULL,
			file_enc_sha256 = NULL,
			file_length = NULL,
			local_path = NULL,
			downloaded_at = NULL,
			media_unavailable_at = NULL,
			edited = 0,
			edited_ts = 0,
			buttons = NULL,
			payload_purged_at = COALESCE(payload_purged_at, ?)
		WHERE chat_jid = ? AND msg_id = ?
	`, purgedAt, chatJID, msgID); err != nil {
		return err
	}
	return tx.Commit()
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
		WHERE m.deleted_at IS NULL`
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
		var deletedAt int64
		var deletionReason string
		var payloadPurgedAt int64
		var buttonsJSON string
		if err := rows.Scan(&m.rowID, &m.ChatJID, &m.ChatName, &m.MsgID, &m.SenderJID, &m.SenderName, &ts, &fromMe, &m.Text, &m.DisplayText, &m.QuotedMsgID, &m.QuotedSenderJID, &forwarded, &forwardingScore, &m.ReactionToID, &m.ReactionEmoji, &m.MediaType, &m.MediaCaption, &m.Filename, &m.MimeType, &m.DirectPath, &m.LocalPath, &downloadedAt, &starred, &starredAt, &revoked, &deletedForMe, &deletedAt, &deletionReason, &payloadPurgedAt, &buttonsJSON, &m.Snippet); err != nil {
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
		m.DeletedAt = timePointerFromUnix(deletedAt)
		m.DeletionReason = deletionReason
		m.PayloadPurgedAt = timePointerFromUnix(payloadPurgedAt)
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
		row.Ts, row.FromMe, row.Text, row.DisplayText, row.QuotedMsgID, row.QuotedSenderJid, row.IsForwarded,
		row.ForwardingScore, row.ReactionToID, row.ReactionEmoji, row.MediaType,
		row.MediaCaption, row.Filename, row.MimeType, row.DirectPath, row.LocalPath,
		row.DownloadedAt, row.Column24, row.StarredAt, row.Revoked, row.DeletedForMe,
		row.DeletedAt, row.DeletionReason, row.PayloadPurgedAt, row.Buttons, row.Column32,
	)
}

func messageFromBeforeRow(row storedb.MessageContextBeforeRow) Message {
	return messageFromScalars(
		row.Rowid, row.ChatJid, row.Name, row.MsgID, row.SenderJid, row.SenderName,
		row.Ts, row.FromMe, row.Text, row.DisplayText, row.QuotedMsgID, row.QuotedSenderJid, row.IsForwarded,
		row.ForwardingScore, row.ReactionToID, row.ReactionEmoji, row.MediaType,
		row.MediaCaption, row.Filename, row.MimeType, row.DirectPath, row.LocalPath,
		row.DownloadedAt, row.Column24, row.StarredAt, row.Revoked, row.DeletedForMe,
		row.DeletedAt, row.DeletionReason, row.PayloadPurgedAt, row.Buttons, row.Column32,
	)
}

func messageFromAfterRow(row storedb.MessageContextAfterRow) Message {
	return messageFromScalars(
		row.Rowid, row.ChatJid, row.Name, row.MsgID, row.SenderJid, row.SenderName,
		row.Ts, row.FromMe, row.Text, row.DisplayText, row.QuotedMsgID, row.QuotedSenderJid, row.IsForwarded,
		row.ForwardingScore, row.ReactionToID, row.ReactionEmoji, row.MediaType,
		row.MediaCaption, row.Filename, row.MimeType, row.DirectPath, row.LocalPath,
		row.DownloadedAt, row.Column24, row.StarredAt, row.Revoked, row.DeletedForMe,
		row.DeletedAt, row.DeletionReason, row.PayloadPurgedAt, row.Buttons, row.Column32,
	)
}

func messageFromScalars(rowID int64, chatJID, chatName, msgID, senderJID, senderName string, ts, fromMe int64, text, displayText, quotedMsgID, quotedSenderJID string, forwarded, forwardingScore int64, reactionToID, reactionEmoji, mediaType, mediaCaption, filename, mimeType, directPath, localPath string, downloadedAt, starred, starredAt, revoked, deletedForMe, deletedAt int64, deletionReason string, payloadPurgedAt int64, buttonsJSON, snippet string) Message {
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
		QuotedMsgID:     quotedMsgID,
		QuotedSenderJID: quotedSenderJID,
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
		DeletedAt:       timePointerFromUnix(deletedAt),
		DeletionReason:  deletionReason,
		PayloadPurgedAt: timePointerFromUnix(payloadPurgedAt),
		Snippet:         snippet,
	}
	if buttonsJSON != "" {
		_ = json.Unmarshal([]byte(buttonsJSON), &m.Buttons)
	}
	return m
}

func timePointerFromUnix(value int64) *time.Time {
	if value <= 0 {
		return nil
	}
	t := fromUnix(value)
	return &t
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
