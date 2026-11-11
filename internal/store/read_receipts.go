package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	// inboundMessage matches the incoming messages of a chat that can be
	// unread, with or without content wacli could read.
	inboundMessage  = "chat_jid = ? AND from_me = 0 AND revoked = 0 AND deleted_for_me = 0 AND deleted_at IS NULL AND COALESCE(reaction_to_id,'') = '' AND COALESCE(reaction_emoji,'') = ''"
	readableContent = "(COALESCE(display_text,'') != '(message)' OR TRIM(COALESCE(text,'')) != '')"
	receiptEligible = inboundMessage + " AND " + readableContent
)

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

// ExactReceiptSnapshot identifies an exact selection by row: the acknowledged
// rows, oldest first, the position of the newest of them, and the floor, the
// newest eligible message before them, if there is one. The range after the
// floor and through the newest acknowledged message is what settlement treats
// as read.
type ExactReceiptSnapshot struct {
	RowIDs       []int64
	ThroughTS    int64
	ThroughRowID int64
	HasFloor     bool
	FloorTS      int64
	FloorRowID   int64
}

// exactRange matches rows after the snapshot's floor and at or before its
// newest acknowledged message. rangeArgs binds it.
const exactRange = "(ts < ? OR (ts = ? AND rowid <= ?)) AND (? = 0 OR ts > ? OR (ts = ? AND rowid > ?))"

func (s ExactReceiptSnapshot) rangeArgs() []any {
	return []any{s.ThroughTS, s.ThroughTS, s.ThroughRowID, s.floorFlag(), s.FloorTS, s.FloorTS, s.FloorRowID}
}

func (s ExactReceiptSnapshot) floorFlag() int {
	if s.HasFloor {
		return 1
	}
	return 0
}

// ErrNotOldestUnread rejects an exact selection that is not the chat's oldest
// unread messages. Acknowledging it could skip an older unread message, or
// acknowledge one that is already read locally; nothing is sent.
var ErrNotOldestUnread = errors.New("the requested messages are not this chat's oldest unread messages")

// ReadReceiptCandidatesExact resolves exactly the requested inbound messages in
// one snapshot. They must be the oldest unread messages of the chat, in any
// order, so the receipts cannot skip an unread message before them. No message
// without readable content may sit among them or between them and the last
// read message either: it may be unread, and its content can still arrive.
// Nothing is selected beyond the requested IDs.
func (d *DB) ReadReceiptCandidatesExact(jid string, ids []string) ([]MessageInfo, ExactReceiptSnapshot, error) {
	if len(ids) == 0 {
		return nil, ExactReceiptSnapshot{}, fmt.Errorf("at least one message ID is required")
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return nil, ExactReceiptSnapshot{}, fmt.Errorf("--message-id requires exact nonempty message IDs")
		}
		if want[id] {
			return nil, ExactReceiptSnapshot{}, fmt.Errorf("message ID %q is repeated", id)
		}
		want[id] = true
	}
	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, ExactReceiptSnapshot{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow("SELECT unread_count FROM chats WHERE jid = ?", jid).Scan(&count); err != nil {
		return nil, ExactReceiptSnapshot{}, err
	}
	if count < len(ids) {
		return nil, ExactReceiptSnapshot{}, fmt.Errorf("%w: chat %s has %d unread messages and %d were requested", ErrNotOldestUnread, jid, count, len(ids))
	}
	var available int
	if err := tx.QueryRow("SELECT COUNT(*) FROM messages WHERE "+receiptEligible, jid).Scan(&available); err != nil {
		return nil, ExactReceiptSnapshot{}, err
	}
	if count > available {
		return nil, ExactReceiptSnapshot{}, fmt.Errorf("chat has %d unread messages but only %d eligible messages stored; sync its history before sending receipts", count, available)
	}
	rows, err := tx.Query("SELECT msg_id, COALESCE(sender_jid,''), ts, rowid FROM (SELECT msg_id, sender_jid, ts, rowid FROM messages WHERE "+receiptEligible+" ORDER BY ts DESC, rowid DESC LIMIT ?) ORDER BY ts ASC, rowid ASC LIMIT ?", jid, count, len(ids))
	if err != nil {
		return nil, ExactReceiptSnapshot{}, err
	}
	defer rows.Close()
	var messages []MessageInfo
	var snap ExactReceiptSnapshot
	var oldestTS, oldestRowID int64
	for rows.Next() {
		m := MessageInfo{ChatJID: jid}
		var ts, rowID int64
		if err := rows.Scan(&m.MsgID, &m.SenderJID, &ts, &rowID); err != nil {
			return nil, ExactReceiptSnapshot{}, err
		}
		if !want[m.MsgID] {
			return nil, ExactReceiptSnapshot{}, fmt.Errorf("%w: unread message %s in chat %s comes first", ErrNotOldestUnread, m.MsgID, jid)
		}
		m.Timestamp = fromUnix(ts)
		if len(messages) == 0 {
			oldestTS, oldestRowID = ts, rowID
		}
		messages = append(messages, m)
		snap.RowIDs = append(snap.RowIDs, rowID)
		snap.ThroughTS, snap.ThroughRowID = ts, rowID
	}
	if err := rows.Err(); err != nil {
		return nil, ExactReceiptSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return nil, ExactReceiptSnapshot{}, err
	}
	if len(messages) != len(ids) {
		return nil, ExactReceiptSnapshot{}, fmt.Errorf("%w: chat %s", ErrNotOldestUnread, jid)
	}
	err = tx.QueryRow("SELECT ts, rowid FROM messages WHERE "+receiptEligible+" AND (ts < ? OR (ts = ? AND rowid < ?)) ORDER BY ts DESC, rowid DESC LIMIT 1", jid, oldestTS, oldestTS, oldestRowID).Scan(&snap.FloorTS, &snap.FloorRowID)
	switch {
	case err == nil:
		snap.HasFloor = true
	case !errors.Is(err, sql.ErrNoRows):
		return nil, ExactReceiptSnapshot{}, err
	}
	var contentless string
	err = tx.QueryRow("SELECT msg_id FROM messages WHERE "+inboundMessage+" AND NOT "+readableContent+" AND "+exactRange+" ORDER BY ts, rowid LIMIT 1", append([]any{jid}, snap.rangeArgs()...)...).Scan(&contentless)
	switch {
	case err == nil:
		return nil, ExactReceiptSnapshot{}, fmt.Errorf("%w: message %s in chat %s has no readable content and may be unread; it comes before %s", ErrNotOldestUnread, contentless, jid, messages[len(messages)-1].MsgID)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, ExactReceiptSnapshot{}, err
	}
	return messages, snap, tx.Commit()
}

// SettleExactReceipts lowers the local unread count after an exact selection
// was acknowledged, but only if the range it treats as read still holds exactly
// the acknowledged messages: the same rows, none revoked or deleted, no other
// message stored there meanwhile, and no message without readable content
// there, whose content could still arrive and be counted. The count must also
// still be exactly those messages plus every newer one. Then only the newer
// messages stay unread. Otherwise the count is kept, because a message that
// arrived during dispatch could not be told apart from an acknowledged one. It
// reports whether the count was lowered. Counting a live arrival is a separate
// step after storing it, so callers must not settle between the two.
func (d *DB) SettleExactReceipts(jid string, snap ExactReceiptSnapshot) (bool, error) {
	k := len(snap.RowIDs)
	if k == 0 {
		return false, fmt.Errorf("no acknowledged messages to settle")
	}
	args := []any{snap.ThroughTS, snap.ThroughRowID, snap.floorFlag(), snap.FloorTS, snap.FloorRowID, jid}
	for _, rowID := range snap.RowIDs {
		args = append(args, rowID)
	}
	args = append(args, jid, k, k, k)
	res, err := d.sql.ExecContext(storeCtx(), `
  WITH bounds AS (SELECT ? AS through_ts, ? AS through_row, ? AS has_floor, ? AS floor_ts, ? AS floor_row),
  placed AS (
   SELECT messages.rowid AS row_id, `+readableContent+` AS readable,
    (messages.ts < bounds.through_ts OR (messages.ts = bounds.through_ts AND messages.rowid <= bounds.through_row)) AS through,
    (bounds.has_floor = 0 OR messages.ts > bounds.floor_ts OR (messages.ts = bounds.floor_ts AND messages.rowid > bounds.floor_row)) AS above_floor
   FROM messages, bounds WHERE `+inboundMessage+`
  ),
  facts AS (
   SELECT
    COALESCE(SUM(readable AND NOT through), 0) AS newer,
    COALESCE(SUM(readable AND through AND above_floor), 0) AS in_range,
    COALESCE(SUM(readable AND through AND above_floor AND row_id IN (`+strings.TrimSuffix(strings.Repeat("?,", k), ",")+`)), 0) AS acknowledged,
    COALESCE(SUM(NOT readable AND through AND above_floor), 0) AS contentless
   FROM placed
  )
  UPDATE chats SET unread_count = facts.newer, unread = facts.newer > 0
  FROM facts
  WHERE chats.jid = ? AND facts.in_range = ? AND facts.acknowledged = ? AND facts.contentless = 0
   AND chats.unread_count = ? + facts.newer`, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
