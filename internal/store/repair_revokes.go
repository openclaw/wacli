package store

import (
	"fmt"
	"strings"
)

// MisfiledRevoke is a revoke that an older parser filed as a new row in the
// owner's own chat (from_me=1) instead of marking the original message in the
// sender's chat.
type MisfiledRevoke struct {
	MisfiledChatJID string `json:"misfiled_chat_jid"`
	MsgID           string `json:"msg_id"`
	TargetChatJID   string `json:"target_chat_jid"`
	DeletedAt       int64  `json:"deleted_at"`
	OriginalText    string `json:"original_text"`
}

// The original must exist in the revoker's chat (sender_jid) and not be
// purged. Matching on an existing original keeps this independent of knowing
// the owner's JID: a legitimate row never has its original in the chat named
// by its own sender in a different chat.
const misfiledRevokesWhere = `
	FROM messages r
	JOIN messages o ON o.chat_jid = r.sender_jid AND o.msg_id = r.msg_id
	WHERE r.revoked != 0 AND r.deletion_reason = ? AND r.from_me != 0
	  AND COALESCE(r.sender_jid,'') != '' AND r.sender_jid != r.chat_jid
	  AND r.chat_jid NOT LIKE '%@g.us' AND r.sender_jid NOT LIKE '%@g.us'
	  AND o.chat_jid NOT LIKE '%@g.us'
	  AND o.payload_purged_at IS NULL`

// FindMisfiledRevokes lists revoke rows filed under the wrong chat.
func (d *DB) FindMisfiledRevokes() ([]MisfiledRevoke, error) {
	rows, err := d.sql.Query(`SELECT r.chat_jid, r.msg_id, o.chat_jid, COALESCE(r.deleted_at,0), COALESCE(o.text,'')`+
		misfiledRevokesWhere+` ORDER BY r.rowid`, MessageDeletionReasonWhatsAppRevoke)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MisfiledRevoke
	for rows.Next() {
		var m MisfiledRevoke
		if err := rows.Scan(&m.MisfiledChatJID, &m.MsgID, &m.TargetChatJID, &m.DeletedAt, &m.OriginalText); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RepairMisfiledRevokes marks each original message revoked (keeping its text
// and media) and removes the misfiled row, all in one transaction.
func (d *DB) RepairMisfiledRevokes() (int, error) {
	found, err := d.FindMisfiledRevokes()
	if err != nil {
		return 0, err
	}
	if len(found) == 0 {
		return 0, nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, m := range found {
		deletedAt := m.DeletedAt
		if deletedAt == 0 {
			deletedAt = nowUTC().Unix()
		}
		res, err := tx.Exec(`UPDATE messages
			SET revoked = 1,
			    deleted_at = COALESCE(deleted_at, ?),
			    deletion_reason = CASE WHEN deleted_at IS NOT NULL THEN COALESCE(NULLIF(deletion_reason,''), ?) ELSE ? END
			WHERE chat_jid = ? AND msg_id = ? AND payload_purged_at IS NULL`,
			deletedAt, MessageDeletionReasonWhatsAppRevoke, MessageDeletionReasonWhatsAppRevoke, m.TargetChatJID, m.MsgID)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return 0, fmt.Errorf("repair revoke %s in %s: original not updated", strings.TrimSpace(m.MsgID), m.TargetChatJID)
		}
		if _, err := tx.Exec(`DELETE FROM messages WHERE chat_jid = ? AND msg_id = ?`, m.MisfiledChatJID, m.MsgID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(found), nil
}
