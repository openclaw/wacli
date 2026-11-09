package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// The label each member last showed under their name in a group. Participant
// snapshots are replaced wholesale on refresh and live group info carries no
// labels, so labels live in their own table and survive a refresh.
const groupMemberLabelsSchema = `
CREATE TABLE IF NOT EXISTS group_member_labels (
    group_jid TEXT NOT NULL,
    user_jid TEXT NOT NULL,
    label TEXT NOT NULL, -- empty when the member cleared it
    label_at INTEGER NOT NULL, -- time of the newest evidence, 0 when unknown
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (group_jid, user_jid)
);

CREATE INDEX IF NOT EXISTS idx_group_member_labels_user ON group_member_labels(user_jid);
`

// ensureGroupMemberLabelsTable adds the label table to stores created before
// it existed. The core schema only runs once, so a new table needs its own step.
func ensureGroupMemberLabelsTable(d *DB) error {
	if _, err := d.sql.Exec(groupMemberLabelsSchema); err != nil {
		return fmt.Errorf("ensure group member labels: %w", err)
	}
	return nil
}

type RecordGroupMemberLabelParams struct {
	GroupJID string
	UserJID  string
	Label    string
	// At is when the label was known to be in effect: the message time, or the
	// change time WhatsApp reported. Zero when nothing dates it.
	At time.Time
}

// RecordGroupMemberLabel keeps the newest label seen for a member. Observations
// arrive in any order (history sync is not chronological), so a label replaces
// the stored one only when it is dated later; an undated label only fills a gap
// and never replaces a dated one. Equal times resolve by label so the result
// does not depend on arrival order.
func (d *DB) RecordGroupMemberLabel(p RecordGroupMemberLabelParams) error {
	groupJID := strings.TrimSpace(p.GroupJID)
	userJID := strings.TrimSpace(p.UserJID)
	if groupJID == "" || userJID == "" {
		return fmt.Errorf("group and user JID are required")
	}
	var at int64
	if !p.At.IsZero() {
		at = unix(p.At)
	}
	_, err := d.sql.Exec(`
		INSERT INTO group_member_labels(group_jid, user_jid, label, label_at, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(group_jid, user_jid) DO UPDATE SET
			label = excluded.label,
			label_at = excluded.label_at,
			updated_at = excluded.updated_at
		WHERE excluded.label_at > group_member_labels.label_at
			OR (excluded.label_at = group_member_labels.label_at AND excluded.label > group_member_labels.label)
	`, groupJID, userJID, strings.TrimSpace(p.Label), at, unix(nowUTC()))
	if err != nil {
		return fmt.Errorf("record group member label: %w", err)
	}
	return nil
}

type groupMemberLabel struct {
	label string
	at    time.Time
}

func (d *DB) groupMemberLabels(groupJID string) (map[string]groupMemberLabel, error) {
	out := map[string]groupMemberLabel{}
	if !d.memberLabelsEnabled {
		return out, nil
	}
	rows, err := d.readQuery().Query(`
		SELECT user_jid, label, label_at FROM group_member_labels
		WHERE group_jid = ? AND label <> ''
	`, groupJID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var user, label string
		var at int64
		if err := rows.Scan(&user, &label, &at); err != nil {
			return nil, err
		}
		l := groupMemberLabel{label: label}
		if at > 0 {
			l.at = fromUnix(at)
		}
		out[user] = l
	}
	return out, rows.Err()
}

func migrateLIDGroupMemberLabelsToPN(tx *sql.Tx, lidJID, pnJID string) error {
	if _, err := tx.Exec(`
		INSERT INTO group_member_labels(group_jid, user_jid, label, label_at, updated_at)
		SELECT group_jid, ?, label, label_at, updated_at
		FROM group_member_labels
		WHERE user_jid = ?
		ON CONFLICT(group_jid, user_jid) DO UPDATE SET
			label = excluded.label,
			label_at = excluded.label_at,
			updated_at = excluded.updated_at
		WHERE excluded.label_at > group_member_labels.label_at
			OR (excluded.label_at = group_member_labels.label_at AND excluded.label > group_member_labels.label)
	`, pnJID, lidJID); err != nil {
		return fmt.Errorf("merge lid group member labels: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM group_member_labels WHERE user_jid = ?`, lidJID); err != nil {
		return fmt.Errorf("delete lid group member labels: %w", err)
	}
	return nil
}
