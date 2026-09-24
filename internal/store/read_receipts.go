package store

import (
	"database/sql"
	"fmt"
)

// ReadReceiptCandidates takes one snapshot of the unread count and eligible
// messages, then returns the oldest bounded slice so repeated calls progress.
func (d *DB) ReadReceiptCandidates(jid string, limit int) ([]MessageInfo, error) {
	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count int
	var marked bool
	if err := tx.QueryRow("SELECT unread_count, unread FROM chats WHERE jid = ?", jid).Scan(&count, &marked); err != nil {
		return nil, err
	}
	if count == 0 && !marked {
		return nil, tx.Commit()
	}
	const eligible = "chat_jid = ? AND from_me = 0 AND revoked = 0 AND deleted_for_me = 0 AND deleted_at IS NULL AND COALESCE(reaction_to_id,'') = '' AND COALESCE(reaction_emoji,'') = '' AND (COALESCE(display_text,'') != '(message)' OR TRIM(COALESCE(text,'')) != '')"
	var available int
	if err := tx.QueryRow("SELECT COUNT(*) FROM messages WHERE "+eligible, jid).Scan(&available); err != nil {
		return nil, err
	}
	if count > available {
		return nil, fmt.Errorf("chat has %d unread messages but only %d eligible messages stored; sync its history before sending receipts", count, available)
	}
	want := count
	if want == 0 {
		want = 1
	}
	rows, err := tx.Query("SELECT msg_id, COALESCE(sender_jid,''), ts FROM (SELECT msg_id, sender_jid, ts, rowid FROM messages WHERE "+eligible+" ORDER BY ts DESC, rowid DESC LIMIT ?) ORDER BY ts ASC, rowid ASC LIMIT ?", jid, want, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []MessageInfo
	for rows.Next() {
		m := MessageInfo{ChatJID: jid}
		var ts int64
		if err := rows.Scan(&m.MsgID, &m.SenderJID, &ts); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return messages, tx.Commit()
}

func (d *DB) ClearChatUnreadMarker(jid string) error {
	_, err := d.sql.ExecContext(storeCtx(), "UPDATE chats SET unread = unread_count > 0 WHERE jid = ?", jid)
	return err
}
