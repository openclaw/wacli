package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// HistoricalLIDJIDs returns distinct hidden-user JIDs stored in chat and
// message identity columns. The app layer resolves these through whatsmeow.
func (d *DB) HistoricalLIDJIDs() ([]string, error) {
	rows, err := d.sql.Query(`
		SELECT jid FROM chats WHERE jid GLOB '*@lid'
		UNION
		SELECT chat_jid FROM messages WHERE chat_jid GLOB '*@lid'
		UNION
		SELECT sender_jid FROM messages WHERE sender_jid GLOB '*@lid'
		ORDER BY 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var jid sql.NullString
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		if jid.Valid {
			if s := strings.TrimSpace(jid.String); s != "" {
				out = append(out, s)
			}
		}
	}
	return out, rows.Err()
}

// MigrateLIDToPN rewrites one historical hidden-user JID to its phone-number
// JID. It is idempotent and merges duplicate chat/message rows created by the
// old split storage behavior.
func (d *DB) MigrateLIDToPN(lidJID, pnJID string) error {
	lidJID = strings.TrimSpace(lidJID)
	pnJID = strings.TrimSpace(pnJID)
	if lidJID == "" || pnJID == "" {
		return fmt.Errorf("lid and phone-number JIDs are required")
	}
	if lidJID == pnJID {
		return nil
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	if err := migrateLIDChatToPN(tx, lidJID, pnJID); err != nil {
		return err
	}
	if err := migrateLIDMessagesToPN(tx, lidJID, pnJID); err != nil {
		return err
	}
	if err := migrateLIDSenderToPN(tx, lidJID, pnJID); err != nil {
		return err
	}
	if err := deleteLIDChat(tx, lidJID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func migrateLIDChatToPN(tx *sql.Tx, lidJID, pnJID string) error {
	if _, err := tx.Exec(`
		INSERT INTO chats(jid, kind, name, last_message_ts)
		SELECT
			?,
			CASE WHEN kind = '' OR kind = 'unknown' THEN 'dm' ELSE kind END,
			name,
			last_message_ts
		FROM chats
		WHERE jid = ?
		ON CONFLICT(jid) DO UPDATE SET
			kind = CASE
				WHEN chats.kind = '' OR chats.kind = 'unknown' OR excluded.kind = 'dm' THEN excluded.kind
				ELSE chats.kind
			END,
			name = CASE
				WHEN excluded.name IS NOT NULL
					AND excluded.name != ''
					AND (
						chats.name IS NULL
						OR chats.name = ''
						OR chats.name = chats.jid
						OR instr(chats.name, '@') > 0
					)
				THEN excluded.name
				ELSE chats.name
			END,
			last_message_ts = max(COALESCE(chats.last_message_ts, 0), COALESCE(excluded.last_message_ts, 0))
	`, pnJID, lidJID); err != nil {
		return fmt.Errorf("merge lid chat into pn chat: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO chats(jid, kind, name, last_message_ts)
		SELECT
			?,
			'dm',
			NULLIF(chat_name, ''),
			ts
		FROM messages
		WHERE chat_jid = ?
		ORDER BY ts DESC, rowid DESC
		LIMIT 1
		ON CONFLICT(jid) DO UPDATE SET
			name = CASE
				WHEN excluded.name IS NOT NULL
					AND excluded.name != ''
					AND (
						chats.name IS NULL
						OR chats.name = ''
						OR chats.name = chats.jid
						OR instr(chats.name, '@') > 0
					)
				THEN excluded.name
				ELSE chats.name
			END,
			last_message_ts = max(COALESCE(chats.last_message_ts, 0), COALESCE(excluded.last_message_ts, 0))
	`, pnJID, lidJID); err != nil {
		return fmt.Errorf("create pn chat from lid messages: %w", err)
	}

	return nil
}

func migrateLIDMessagesToPN(tx *sql.Tx, lidJID, pnJID string) error {
	if _, err := tx.Exec(`
		INSERT INTO messages(
			chat_jid, chat_name, msg_id, sender_jid, sender_name, ts, from_me, text, display_text,
			is_forwarded, forwarding_score, reaction_to_id, reaction_emoji,
			media_type, media_caption, filename, mime_type, direct_path,
			media_key, file_sha256, file_enc_sha256, file_length, local_path, downloaded_at
		)
		SELECT
			?,
			chat_name,
			msg_id,
			CASE WHEN sender_jid = ? THEN ? ELSE sender_jid END,
			sender_name,
			ts,
			from_me,
			text,
			display_text,
			is_forwarded,
			forwarding_score,
			reaction_to_id,
			reaction_emoji,
			media_type,
			media_caption,
			filename,
			mime_type,
			direct_path,
			media_key,
			file_sha256,
			file_enc_sha256,
			file_length,
			local_path,
			downloaded_at
		FROM messages
		WHERE chat_jid = ?
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			chat_name = COALESCE(NULLIF(messages.chat_name, ''), excluded.chat_name),
			sender_jid = COALESCE(NULLIF(messages.sender_jid, ''), excluded.sender_jid),
			sender_name = COALESCE(NULLIF(messages.sender_name, ''), excluded.sender_name),
			ts = max(messages.ts, excluded.ts),
			from_me = messages.from_me,
			text = COALESCE(NULLIF(messages.text, ''), excluded.text),
			display_text = COALESCE(NULLIF(messages.display_text, ''), excluded.display_text),
			is_forwarded = CASE WHEN messages.is_forwarded != 0 THEN messages.is_forwarded ELSE excluded.is_forwarded END,
			forwarding_score = max(messages.forwarding_score, excluded.forwarding_score),
			reaction_to_id = COALESCE(NULLIF(messages.reaction_to_id, ''), excluded.reaction_to_id),
			reaction_emoji = COALESCE(NULLIF(messages.reaction_emoji, ''), excluded.reaction_emoji),
			media_type = COALESCE(NULLIF(messages.media_type, ''), excluded.media_type),
			media_caption = COALESCE(NULLIF(messages.media_caption, ''), excluded.media_caption),
			filename = COALESCE(NULLIF(messages.filename, ''), excluded.filename),
			mime_type = COALESCE(NULLIF(messages.mime_type, ''), excluded.mime_type),
			direct_path = COALESCE(NULLIF(messages.direct_path, ''), excluded.direct_path),
			media_key = CASE WHEN messages.media_key IS NOT NULL AND length(messages.media_key) > 0 THEN messages.media_key ELSE excluded.media_key END,
			file_sha256 = CASE WHEN messages.file_sha256 IS NOT NULL AND length(messages.file_sha256) > 0 THEN messages.file_sha256 ELSE excluded.file_sha256 END,
			file_enc_sha256 = CASE WHEN messages.file_enc_sha256 IS NOT NULL AND length(messages.file_enc_sha256) > 0 THEN messages.file_enc_sha256 ELSE excluded.file_enc_sha256 END,
			file_length = CASE WHEN messages.file_length IS NOT NULL AND messages.file_length > 0 THEN messages.file_length ELSE excluded.file_length END,
			local_path = COALESCE(NULLIF(messages.local_path, ''), excluded.local_path),
			downloaded_at = CASE WHEN messages.downloaded_at IS NOT NULL AND messages.downloaded_at > 0 THEN messages.downloaded_at ELSE excluded.downloaded_at END
	`, pnJID, lidJID, pnJID, lidJID); err != nil {
		return fmt.Errorf("merge lid messages into pn chat: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM messages WHERE chat_jid = ?`, lidJID); err != nil {
		return fmt.Errorf("delete migrated lid messages: %w", err)
	}
	return nil
}

func migrateLIDSenderToPN(tx *sql.Tx, lidJID, pnJID string) error {
	if _, err := tx.Exec(`UPDATE messages SET sender_jid = ? WHERE sender_jid = ?`, pnJID, lidJID); err != nil {
		return fmt.Errorf("rewrite lid message senders: %w", err)
	}
	return nil
}

func deleteLIDChat(tx *sql.Tx, lidJID string) error {
	if _, err := tx.Exec(`DELETE FROM chats WHERE jid = ?`, lidJID); err != nil {
		return fmt.Errorf("delete migrated lid chat: %w", err)
	}
	return nil
}
