package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func migrateQuickReplies(d *DB) error {
	_, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS quick_replies (
			id TEXT PRIMARY KEY, shortcut TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '',
			keywords TEXT NOT NULL DEFAULT '[]', label_ids TEXT NOT NULL DEFAULT '[]', count INTEGER,
			deleted INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS quick_reply_sync_state (
			id INTEGER PRIMARY KEY CHECK(id=1), full_sync_events_seen INTEGER NOT NULL DEFAULT 0
		);
		INSERT OR IGNORE INTO quick_reply_sync_state(id) VALUES(1);
	`)
	return err
}

// QuickReply is one observed WhatsApp Business quick reply. Like lists, the
// local index only knows what sync has delivered.
type QuickReply struct {
	ID       string   `json:"id"`
	Shortcut string   `json:"shortcut"`
	Message  string   `json:"message"`
	Keywords []string `json:"keywords"`
	LabelIDs []string `json:"label_ids"`
	Count    *int32   `json:"count"`
	Deleted  bool     `json:"deleted,omitempty"`
}

type QuickRepliesResult struct {
	QuickReplies []QuickReply `json:"quick_replies"`
	Completeness string       `json:"completeness"`
}

var ErrQuickReplyNotFound = errors.New("quick reply not found")

// UpsertQuickReply stores a whole quick reply value: WhatsApp replaces the
// complete action on every change, so absent fields are not merged. The shared
// app-state persistence owner supplies authoritative application order; device
// timestamps are retained as data only, just like contact SET actions.
func (d *DB) UpsertQuickReply(q QuickReply, at time.Time) error {
	if strings.TrimSpace(q.ID) == "" {
		return fmt.Errorf("quick reply ID is required")
	}
	keywords, err := json.Marshal(nonNilStrings(q.Keywords))
	if err != nil {
		return err
	}
	labels, err := json.Marshal(nonNilStrings(q.LabelIDs))
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT INTO quick_replies(id,shortcut,message,keywords,label_ids,count,deleted,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET shortcut=excluded.shortcut, message=excluded.message,
			keywords=excluded.keywords, label_ids=excluded.label_ids, count=excluded.count,
			deleted=excluded.deleted, updated_at=excluded.updated_at`,
		q.ID, q.Shortcut, q.Message, string(keywords), string(labels), q.Count, q.Deleted, at.UnixMilli())
	return err
}

// QuickReplies returns the observed, not deleted quick replies.
func (d *DB) QuickReplies() (QuickRepliesResult, error) {
	result := QuickRepliesResult{QuickReplies: []QuickReply{}, Completeness: "incomplete"}
	// Read-only opens never migrate, so tolerate a store without the table.
	exists, err := d.tableExists("quick_replies")
	if err != nil || !exists {
		return result, err
	}
	coverageExists, err := d.tableExists("quick_reply_sync_state")
	if err != nil {
		return result, err
	}
	if coverageExists {
		var seen bool
		if err := d.sql.QueryRow(`SELECT full_sync_events_seen FROM quick_reply_sync_state WHERE id=1`).Scan(&seen); err != nil && err != sql.ErrNoRows {
			return result, err
		}
		if seen {
			result.Completeness = "complete"
		}
	}
	rows, err := d.sql.Query(`SELECT id,shortcut,message,keywords,label_ids,count,deleted FROM quick_replies WHERE deleted=0 ORDER BY shortcut,id`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		q, err := scanQuickReply(rows)
		if err != nil {
			return result, err
		}
		result.QuickReplies = append(result.QuickReplies, q)
	}
	return result, rows.Err()
}

// MarkRegularFullSyncEventsSeen records a successfully persisted full replay,
// including empty snapshots. Quick-reply coverage never inherits Lists history.
func (d *DB) MarkRegularFullSyncEventsSeen() error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE list_sync_state SET full_sync_events_seen=1 WHERE id=1`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE quick_reply_sync_state SET full_sync_events_seen=1 WHERE id=1`); err != nil {
		return err
	}
	return tx.Commit()
}

// QuickReplyByID returns a stored quick reply, including deleted ones.
func (d *DB) QuickReplyByID(id string) (QuickReply, bool, error) {
	exists, err := d.tableExists("quick_replies")
	if err != nil || !exists {
		return QuickReply{}, false, err
	}
	row := d.sql.QueryRow(`SELECT id,shortcut,message,keywords,label_ids,count,deleted FROM quick_replies WHERE id=?`, id)
	q, err := scanQuickReply(row)
	if err == sql.ErrNoRows {
		return QuickReply{}, false, nil
	}
	return q, err == nil, err
}

// ResolveQuickReply accepts an exact ID first, then an exact shortcut, with
// or without its leading slash. Duplicate shortcuts require an ID.
func (d *DB) ResolveQuickReply(value string) (QuickReply, error) {
	result, err := d.QuickReplies()
	if err != nil {
		return QuickReply{}, err
	}
	var matches []QuickReply
	for _, q := range result.QuickReplies {
		if q.ID == value {
			return q, nil
		}
		if q.Shortcut == value || strings.TrimPrefix(q.Shortcut, "/") == strings.TrimPrefix(value, "/") {
			matches = append(matches, q)
		}
	}
	if len(matches) == 0 {
		return QuickReply{}, fmt.Errorf("quick reply %q is not in the local index; sync first: %w", value, ErrQuickReplyNotFound)
	}
	if len(matches) > 1 {
		return QuickReply{}, fmt.Errorf("quick reply shortcut %q is ambiguous; use an ID", value)
	}
	return matches[0], nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanQuickReply(row rowScanner) (QuickReply, error) {
	var q QuickReply
	var keywords, labels string
	if err := row.Scan(&q.ID, &q.Shortcut, &q.Message, &keywords, &labels, &q.Count, &q.Deleted); err != nil {
		return QuickReply{}, err
	}
	if err := json.Unmarshal([]byte(keywords), &q.Keywords); err != nil {
		return QuickReply{}, fmt.Errorf("decode quick reply %s keywords: %w", q.ID, err)
	}
	if err := json.Unmarshal([]byte(labels), &q.LabelIDs); err != nil {
		return QuickReply{}, fmt.Errorf("decode quick reply %s labels: %w", q.ID, err)
	}
	q.Keywords, q.LabelIDs = nonNilStrings(q.Keywords), nonNilStrings(q.LabelIDs)
	return q, nil
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
