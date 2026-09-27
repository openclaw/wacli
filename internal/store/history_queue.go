package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HistorySyncQueueItem is a history sync notification waiting to be stored.
//
// The queue lives in the store rather than in memory because whatsmeow
// acknowledges a notification as soon as it arrives: a chunk held only in
// memory would be lost for good if the process stopped before storing it,
// while a queued one is picked up by the next sync.
type HistorySyncQueueItem struct {
	ID    int64
	MsgID string
	// AccountJID is the linked account the notification was received for. The
	// row outlives the session, so it must not be stored for any other one.
	AccountJID   string
	SyncType     int32
	Notification []byte
	QueuedAt     time.Time
	Attempts     int
}

// EnqueueHistorySync adds a marshaled history sync notification, received for
// accountJID, to the queue. A notification already queued under the same
// message ID is kept once, so a redelivered notification is not stored twice.
func (d *DB) EnqueueHistorySync(msgID, accountJID string, syncType int32, notification []byte, queuedAt time.Time) error {
	if len(notification) == 0 {
		return fmt.Errorf("history sync notification is required")
	}
	if strings.TrimSpace(accountJID) == "" {
		return fmt.Errorf("history sync account is required")
	}
	if _, err := d.sql.Exec(`
		INSERT INTO history_sync_queue(msg_id, account_jid, sync_type, notification, queued_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(msg_id) DO NOTHING
	`, nullIfEmpty(msgID), strings.TrimSpace(accountJID), syncType, notification, unix(queuedAt)); err != nil {
		return fmt.Errorf("queue history sync: %w", err)
	}
	return nil
}

// NextHistorySync returns the oldest queued notification whose ID is not in
// skip; ok is false when there is none.
func (d *DB) NextHistorySync(skip []int64) (item HistorySyncQueueItem, ok bool, err error) {
	query := `
		SELECT id, msg_id, account_jid, sync_type, notification, queued_at, attempts
		FROM history_sync_queue`
	args := make([]any, 0, len(skip))
	if len(skip) > 0 {
		query += ` WHERE id NOT IN (?` + strings.Repeat(`, ?`, len(skip)-1) + `)`
		for _, id := range skip {
			args = append(args, id)
		}
	}
	query += ` ORDER BY id LIMIT 1`
	var msgID sql.NullString
	var queuedAt int64
	err = d.sql.QueryRow(query, args...).Scan(&item.ID, &msgID, &item.AccountJID, &item.SyncType, &item.Notification, &queuedAt, &item.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return HistorySyncQueueItem{}, false, nil
	}
	if err != nil {
		return HistorySyncQueueItem{}, false, fmt.Errorf("read history sync queue: %w", err)
	}
	item.MsgID = msgID.String
	item.QueuedAt = fromUnix(queuedAt)
	return item, true, nil
}

// MarkHistorySyncAttempt records a failed attempt at storing a queued
// notification and returns how many attempts it has had.
func (d *DB) MarkHistorySyncAttempt(id int64) (int, error) {
	var attempts int
	if err := d.sql.QueryRow(`
		UPDATE history_sync_queue SET attempts = attempts + 1
		WHERE id = ?
		RETURNING attempts
	`, id).Scan(&attempts); err != nil {
		return 0, fmt.Errorf("count history sync attempt: %w", err)
	}
	return attempts, nil
}

// DeleteHistorySync removes a queued notification once it has been stored, or
// given up on.
func (d *DB) DeleteHistorySync(id int64) error {
	if _, err := d.sql.Exec(`DELETE FROM history_sync_queue WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete queued history sync: %w", err)
	}
	return nil
}

// CountHistorySyncQueue returns how many notifications wait to be stored.
func (d *DB) CountHistorySyncQueue() (int, error) {
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM history_sync_queue`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count history sync queue: %w", err)
	}
	return n, nil
}
