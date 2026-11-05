package store

import (
	"database/sql"
	"fmt"
	"strings"
)

const receiptEligible = "chat_jid = ? AND from_me = 0 AND revoked = 0 AND deleted_for_me = 0 AND deleted_at IS NULL AND " + notReaction + " AND " + notPlaceholder

// ReadBoundary resolves an exact inbound anchor before app-state recovery can
// start. The chat predicate prevents an ID from another chat becoming a cutoff.
func (d *DB) ReadBoundary(jid, id string) (MessageInfo, error) {
	m, _, err := readBoundary(d.sql, jid, id)
	return m, err
}

func readBoundary(db interface {
	QueryRow(string, ...any) *sql.Row
}, jid, id string) (MessageInfo, int64, error) {
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
		return MessageInfo{}, 0, fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	m := MessageInfo{ChatJID: jid}
	var rowID, ts int64
	err := db.QueryRow("SELECT rowid, msg_id, COALESCE(sender_jid,''), ts FROM messages WHERE "+receiptEligible+" AND msg_id = ?", jid, id).Scan(&rowID, &m.MsgID, &m.SenderJID, &ts)
	if err != nil {
		return MessageInfo{}, 0, fmt.Errorf("read boundary %q must be a stored inbound message in chat %s: %w", id, jid, err)
	}
	m.Timestamp = fromUnix(ts)
	if m.Timestamp.IsZero() {
		return MessageInfo{}, 0, fmt.Errorf("message %q in chat %s has no read boundary", id, jid)
	}
	return m, rowID, nil
}

// ReadReceiptCandidates takes one snapshot of the unread count and eligible
// messages, then returns the oldest bounded slice so repeated calls progress.
func (d *DB) ReadReceiptCandidates(jid string, limit int) ([]MessageInfo, error) {
	return d.readReceiptCandidates(jid, "", limit)
}

// ReadReceiptCandidatesThrough intersects the current unread suffix with the
// handled prefix. Both are selected in one snapshot; newer same-second rows are
// excluded using the anchor's row position, and the existing batch cap remains.
func (d *DB) ReadReceiptCandidatesThrough(jid, id string, limit int) ([]MessageInfo, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	return d.readReceiptCandidates(jid, id, limit)
}

func (d *DB) readReceiptCandidates(jid, throughID string, limit int) ([]MessageInfo, error) {
	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	boundary := ""
	var boundaryArgs []any
	if throughID != "" {
		m, rowID, err := readBoundary(tx, jid, throughID)
		if err != nil {
			return nil, err
		}
		boundary = " WHERE ts < ? OR (ts = ? AND rowid <= ?)"
		boundaryArgs = []any{unix(m.Timestamp), unix(m.Timestamp), rowID}
	}
	var count int
	var marked bool
	if err := tx.QueryRow("SELECT unread_count, unread FROM chats WHERE jid = ?", jid).Scan(&count, &marked); err != nil {
		return nil, err
	}
	if count == 0 && !marked {
		return nil, tx.Commit()
	}
	var available int
	if err := tx.QueryRow("SELECT COUNT(*) FROM messages WHERE "+receiptEligible, jid).Scan(&available); err != nil {
		return nil, err
	}
	// With no eligible rows, the caller can clear local metadata without
	// acknowledging any message. Keep the guard for a nonempty partial history:
	// its receipt boundary would otherwise clear the unknown unread remainder.
	if count > available && available > 0 {
		return nil, fmt.Errorf("chat has %d unread messages but only %d eligible messages stored; sync its history before sending receipts", count, available)
	}
	want := count
	if want == 0 {
		want = 1
	}
	args := append([]any{jid, want}, boundaryArgs...)
	args = append(args, limit)
	rows, err := tx.Query("SELECT msg_id, COALESCE(sender_jid,''), ts FROM (SELECT msg_id, sender_jid, ts, rowid FROM messages WHERE "+receiptEligible+" ORDER BY ts DESC, rowid DESC LIMIT ?)"+boundary+" ORDER BY ts ASC, rowid ASC LIMIT ?", args...)
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

// ClearChatUnreadMarker clears only local unread metadata when there is nothing
// to acknowledge. Recheck eligibility atomically so an arrival after selection
// keeps its count or marker. No message read boundary or network state changes.
func (d *DB) ClearChatUnreadMarker(jid string) error {
	_, err := d.sql.ExecContext(storeCtx(), "UPDATE chats SET unread = 0, unread_count = 0 WHERE jid = ? AND NOT EXISTS (SELECT 1 FROM messages WHERE "+receiptEligible+")", jid, jid)
	return err
}
