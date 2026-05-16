package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// HistoricalLIDJIDs returns distinct hidden-user JIDs stored in chat and
// message/poll identity columns. The app layer resolves these through whatsmeow.
func (d *DB) HistoricalLIDJIDs() ([]string, error) {
	rows, err := d.sql.Query(`
		SELECT jid FROM chats WHERE jid GLOB '*@lid'
		UNION
		SELECT chat_jid FROM messages WHERE chat_jid GLOB '*@lid'
		UNION
		SELECT sender_jid FROM messages WHERE sender_jid GLOB '*@lid'
		UNION
		SELECT chat_jid FROM polls WHERE chat_jid GLOB '*@lid'
		UNION
		SELECT sender_jid FROM polls WHERE sender_jid GLOB '*@lid' AND chat_jid NOT GLOB '*@g.us'
		UNION
		SELECT chat_jid FROM poll_votes WHERE chat_jid GLOB '*@lid'
		UNION
		SELECT voter_jid FROM poll_votes WHERE voter_jid GLOB '*@lid'
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
	if err := migrateLIDPollsToPN(tx, lidJID, pnJID); err != nil {
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
			media_key, file_sha256, file_enc_sha256, file_length, local_path, downloaded_at,
			revoked, deleted_for_me, edited, edited_ts, buttons
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
			downloaded_at,
			revoked,
			deleted_for_me,
			edited,
			edited_ts,
			buttons
		FROM messages
		WHERE chat_jid = ?
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			chat_name = COALESCE(NULLIF(messages.chat_name, ''), excluded.chat_name),
			sender_jid = COALESCE(NULLIF(messages.sender_jid, ''), excluded.sender_jid),
			sender_name = COALESCE(NULLIF(messages.sender_name, ''), excluded.sender_name),
			ts = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 THEN messages.ts WHEN excluded.edited != 0 THEN messages.ts WHEN messages.edited != 0 AND excluded.edited = 0 THEN excluded.ts ELSE max(messages.ts, excluded.ts) END,
			from_me = messages.from_me,
			text = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN excluded.edited != 0 AND (messages.edited = 0 OR excluded.edited_ts > messages.edited_ts) THEN excluded.text WHEN messages.edited != 0 AND excluded.edited = 0 THEN messages.text ELSE COALESCE(NULLIF(messages.text, ''), excluded.text) END,
			display_text = CASE WHEN messages.deleted_for_me != 0 OR excluded.deleted_for_me != 0 THEN ? WHEN messages.revoked != 0 OR excluded.revoked != 0 THEN ? WHEN excluded.edited != 0 AND (messages.edited = 0 OR excluded.edited_ts > messages.edited_ts) THEN excluded.display_text WHEN messages.edited != 0 AND excluded.edited = 0 THEN messages.display_text ELSE COALESCE(NULLIF(messages.display_text, ''), excluded.display_text) END,
			is_forwarded = CASE WHEN messages.is_forwarded != 0 THEN messages.is_forwarded ELSE excluded.is_forwarded END,
			forwarding_score = max(messages.forwarding_score, excluded.forwarding_score),
			reaction_to_id = COALESCE(NULLIF(messages.reaction_to_id, ''), excluded.reaction_to_id),
			reaction_emoji = COALESCE(NULLIF(messages.reaction_emoji, ''), excluded.reaction_emoji),
			media_type = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE COALESCE(NULLIF(messages.media_type, ''), excluded.media_type) END,
			media_caption = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE COALESCE(NULLIF(messages.media_caption, ''), excluded.media_caption) END,
			filename = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE COALESCE(NULLIF(messages.filename, ''), excluded.filename) END,
			mime_type = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE COALESCE(NULLIF(messages.mime_type, ''), excluded.mime_type) END,
			direct_path = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE COALESCE(NULLIF(messages.direct_path, ''), excluded.direct_path) END,
			media_key = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN messages.media_key IS NOT NULL AND length(messages.media_key) > 0 THEN messages.media_key ELSE excluded.media_key END,
			file_sha256 = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN messages.file_sha256 IS NOT NULL AND length(messages.file_sha256) > 0 THEN messages.file_sha256 ELSE excluded.file_sha256 END,
			file_enc_sha256 = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN messages.file_enc_sha256 IS NOT NULL AND length(messages.file_enc_sha256) > 0 THEN messages.file_enc_sha256 ELSE excluded.file_enc_sha256 END,
			file_length = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN messages.file_length IS NOT NULL AND messages.file_length > 0 THEN messages.file_length ELSE excluded.file_length END,
			local_path = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE COALESCE(NULLIF(messages.local_path, ''), excluded.local_path) END,
			downloaded_at = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL WHEN messages.downloaded_at IS NOT NULL AND messages.downloaded_at > 0 THEN messages.downloaded_at ELSE excluded.downloaded_at END,
			revoked = CASE WHEN messages.revoked != 0 OR excluded.revoked != 0 THEN 1 ELSE 0 END,
			deleted_for_me = CASE WHEN messages.deleted_for_me != 0 OR excluded.deleted_for_me != 0 THEN 1 ELSE 0 END,
			edited = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN 0 WHEN messages.edited != 0 OR excluded.edited != 0 THEN 1 ELSE 0 END,
			edited_ts = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN 0 ELSE max(COALESCE(messages.edited_ts, 0), COALESCE(excluded.edited_ts, 0)) END,
			buttons = CASE WHEN messages.revoked != 0 OR messages.deleted_for_me != 0 OR excluded.revoked != 0 OR excluded.deleted_for_me != 0 THEN NULL ELSE COALESCE(messages.buttons, excluded.buttons) END
	`, pnJID, lidJID, pnJID, lidJID, DeletedForMeMessageDisplayText, DeletedMessageDisplayText); err != nil {
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

func migrateLIDPollsToPN(tx *sql.Tx, lidJID, pnJID string) error {
	if err := migrateLIDPollRowsToPN(tx, lidJID, pnJID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM polls WHERE chat_jid = ? OR (sender_jid = ? AND chat_jid NOT GLOB '*@g.us')`, lidJID, lidJID); err != nil {
		return fmt.Errorf("delete migrated lid polls: %w", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO poll_votes(chat_jid, poll_msg_id, voter_jid, vote_msg_id, selected_options_json, ts)
		SELECT
			CASE WHEN chat_jid = ? THEN ? ELSE chat_jid END,
			poll_msg_id,
			CASE WHEN voter_jid = ? THEN ? ELSE voter_jid END,
			vote_msg_id,
			selected_options_json,
			ts
		FROM poll_votes
		WHERE chat_jid = ? OR voter_jid = ?
		ON CONFLICT(chat_jid, poll_msg_id, voter_jid) DO UPDATE SET
			vote_msg_id = excluded.vote_msg_id,
			selected_options_json = excluded.selected_options_json,
			ts = excluded.ts
		WHERE excluded.ts >= poll_votes.ts
	`, lidJID, pnJID, lidJID, pnJID, lidJID, lidJID); err != nil {
		return fmt.Errorf("merge lid poll votes into pn rows: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM poll_votes WHERE chat_jid = ? OR voter_jid = ?`, lidJID, lidJID); err != nil {
		return fmt.Errorf("delete migrated lid poll votes: %w", err)
	}
	return nil
}

func migrateLIDPollRowsToPN(tx *sql.Tx, lidJID, pnJID string) error {
	rows, err := tx.Query(`
		SELECT chat_jid, msg_id, COALESCE(sender_jid,''), question, options_json, selectable_count, created_ts
		FROM polls
		WHERE chat_jid = ? OR (sender_jid = ? AND chat_jid NOT GLOB '*@g.us')
	`, lidJID, lidJID)
	if err != nil {
		return fmt.Errorf("load lid polls: %w", err)
	}
	defer rows.Close()

	type pollRow struct {
		chatJID         string
		msgID           string
		senderJID       string
		question        string
		optionsJSON     string
		selectableCount int64
		createdTS       int64
	}
	var polls []pollRow
	for rows.Next() {
		var p pollRow
		if err := rows.Scan(&p.chatJID, &p.msgID, &p.senderJID, &p.question, &p.optionsJSON, &p.selectableCount, &p.createdTS); err != nil {
			return fmt.Errorf("scan lid poll: %w", err)
		}
		polls = append(polls, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range polls {
		destChat := p.chatJID
		if destChat == lidJID {
			destChat = pnJID
		}
		destSender := p.senderJID
		if destSender == lidJID && !strings.HasSuffix(p.chatJID, "@g.us") {
			destSender = pnJID
		}
		optionsJSON, err := mergedPollOptionsJSONTx(tx, destChat, p.msgID, p.optionsJSON)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO polls(chat_jid, msg_id, sender_jid, question, options_json, selectable_count, created_ts)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
				sender_jid = COALESCE(NULLIF(excluded.sender_jid, ''), polls.sender_jid),
				question = excluded.question,
				options_json = excluded.options_json,
				selectable_count = excluded.selectable_count,
				created_ts = max(polls.created_ts, excluded.created_ts)
		`, destChat, p.msgID, destSender, p.question, optionsJSON, p.selectableCount, p.createdTS); err != nil {
			return fmt.Errorf("merge lid poll into pn row: %w", err)
		}
	}
	return nil
}

func mergedPollOptionsJSONTx(tx *sql.Tx, chatJID, msgID, incomingJSON string) (string, error) {
	incoming, err := decodePollOptionsJSON(incomingJSON)
	if err != nil {
		return "", err
	}
	var existingJSON string
	err = tx.QueryRow(`SELECT options_json FROM polls WHERE chat_jid = ? AND msg_id = ?`, chatJID, msgID).Scan(&existingJSON)
	if err == nil {
		existing, err := decodePollOptionsJSON(existingJSON)
		if err != nil {
			return "", err
		}
		incoming = mergePollOptions(incoming, existing)
	} else if !isNoRows(err) {
		return "", err
	}
	out, err := json.Marshal(incoming)
	if err != nil {
		return "", fmt.Errorf("marshal migrated poll options: %w", err)
	}
	return string(out), nil
}

func decodePollOptionsJSON(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var options []string
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil, fmt.Errorf("unmarshal migrated poll options: %w", err)
	}
	return options, nil
}

func isNoRows(err error) bool {
	return err == sql.ErrNoRows
}

func deleteLIDChat(tx *sql.Tx, lidJID string) error {
	if _, err := tx.Exec(`DELETE FROM chats WHERE jid = ?`, lidJID); err != nil {
		return fmt.Errorf("delete migrated lid chat: %w", err)
	}
	return nil
}
