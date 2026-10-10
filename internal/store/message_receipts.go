package store

import (
	"fmt"
	"strings"
	"time"
)

// receiptRank orders how far a message has travelled. WhatsApp never goes back,
// so a later receipt for the same recipient only counts when it reaches further.
var receiptRank = map[string]int{"delivered": 1, "read": 2, "played": 3}

// UpsertMessageReceipt records what one recipient reported about one message.
func (d *DB) UpsertMessageReceipt(chatJID, msgID, recipientJID, status string, ts time.Time) error {
	chatJID = strings.TrimSpace(chatJID)
	msgID = strings.TrimSpace(msgID)
	recipientJID = strings.TrimSpace(recipientJID)
	status = strings.ToLower(strings.TrimSpace(status))
	if chatJID == "" || msgID == "" || recipientJID == "" {
		return fmt.Errorf("chat JID, message ID and recipient JID are required")
	}
	rank, ok := receiptRank[status]
	if !ok {
		return fmt.Errorf("unknown receipt status %q", status)
	}
	// Only a message of ours that we hold can carry a state worth showing: a
	// report about anything else says nothing about our own ticks.
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO message_receipts(chat_jid, msg_id, recipient_jid, status, ts)
		SELECT ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM messages WHERE chat_jid = ? AND msg_id = ? AND from_me = 1)
		ON CONFLICT(chat_jid, msg_id, recipient_jid) DO UPDATE SET
			status = excluded.status,
			ts = excluded.ts
		WHERE ? > CASE message_receipts.status
			WHEN 'delivered' THEN 1 WHEN 'read' THEN 2 WHEN 'played' THEN 3 ELSE 0 END
	`, chatJID, msgID, recipientJID, status, unix(ts), chatJID, msgID, rank)
	return err
}

// MessageReceiptCounts is how many recipients have reached each state.
type MessageReceiptCounts struct {
	Delivered int
	Read      int
}

// MessageReceipts reports the counts for one message, for callers that hold a
// message without its list columns.
func (d *DB) MessageReceipts(chatJID, msgID string) (MessageReceiptCounts, error) {
	var counts MessageReceiptCounts
	err := d.sql.QueryRowContext(storeCtx(), `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN status IN ('read','played') THEN 1 ELSE 0 END), 0)
		FROM message_receipts WHERE chat_jid = ? AND msg_id = ?
	`, strings.TrimSpace(chatJID), strings.TrimSpace(msgID)).Scan(&counts.Delivered, &counts.Read)
	if err != nil {
		return MessageReceiptCounts{}, err
	}
	return counts, nil
}
