package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Where a member label was seen. A history sync carries the group roster with
// each member's label, a label change is its own control message, and every
// ordinary group message repeats the sender's current label.
const (
	MemberLabelSourceHistorySync = "history_sync"
	MemberLabelSourceChange      = "label_change"
	MemberLabelSourceMessage     = "message"
)

// One row per label a member had in a group, so the label at any point in time
// can be told. Participant snapshots are replaced wholesale on refresh and live
// group info carries no labels, so the history lives in its own table.
const groupMemberLabelsSchema = `
CREATE TABLE IF NOT EXISTS group_member_labels (
    group_jid TEXT NOT NULL,
    user_jid TEXT NOT NULL,
    label TEXT NOT NULL, -- empty when the member cleared it
    set_at INTEGER NOT NULL,
    source TEXT NOT NULL, -- history_sync|label_change|message
    observed_at INTEGER NOT NULL,
    PRIMARY KEY (group_jid, user_jid, set_at, label)
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

type GroupMemberLabel struct {
	GroupJID   string    `json:"group_jid"`
	UserJID    string    `json:"user_jid"`
	Label      string    `json:"label"`
	SetAt      time.Time `json:"set_at"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

type RecordGroupMemberLabelParams struct {
	GroupJID string
	UserJID  string
	Label    string
	Source   string
	// SetAt is when the member set the label, as WhatsApp reported it.
	SetAt time.Time
	// SeenAt stands in for a missing SetAt, typically the message timestamp.
	// Such an observation only says the label was in effect by then, so it is
	// merged into the label already known for that moment instead of becoming
	// a row per message.
	SeenAt time.Time
}

// RecordGroupMemberLabel adds a label to a member's history. Observations may
// arrive in any order (history sync is not chronological), so each one is
// placed by its time: a label equal to the one in effect then adds nothing, and
// an earlier sighting of the label that follows moves that label's estimated
// start back instead of adding a duplicate.
func (d *DB) RecordGroupMemberLabel(p RecordGroupMemberLabelParams) (err error) {
	groupJID := strings.TrimSpace(p.GroupJID)
	userJID := strings.TrimSpace(p.UserJID)
	if groupJID == "" || userJID == "" {
		return fmt.Errorf("group and user JID are required")
	}
	label := strings.TrimSpace(p.Label)
	source := strings.TrimSpace(p.Source)
	at := p.SetAt
	if at.IsZero() {
		at = p.SeenAt
	}
	if at.IsZero() {
		at = nowUTC()
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	prev, hasPrev, err := memberLabelNear(tx, groupJID, userJID, `set_at <= ? ORDER BY set_at DESC, rowid DESC`, unix(at))
	if err != nil {
		return err
	}
	if hasPrev && prev.Label == label {
		return tx.Commit()
	}
	next, hasNext, err := memberLabelNear(tx, groupJID, userJID, `set_at > ? ORDER BY set_at ASC, rowid ASC`, unix(at))
	if err != nil {
		return err
	}
	if hasNext && next.Label == label && next.Source == MemberLabelSourceMessage {
		// The next row only estimated when this label started; it started by now.
		if _, err = tx.Exec(`UPDATE group_member_labels SET set_at = ?, source = ?, observed_at = ? WHERE rowid = ?`,
			unix(at), source, unix(nowUTC()), next.rowID); err != nil {
			return fmt.Errorf("move group member label: %w", err)
		}
		return tx.Commit()
	}
	if _, err = tx.Exec(`
		INSERT OR IGNORE INTO group_member_labels(group_jid, user_jid, label, set_at, source, observed_at)
		VALUES(?, ?, ?, ?, ?, ?)
	`, groupJID, userJID, label, unix(at), source, unix(nowUTC())); err != nil {
		return fmt.Errorf("record group member label: %w", err)
	}
	return tx.Commit()
}

type storedMemberLabel struct {
	GroupMemberLabel
	rowID int64
}

func memberLabelNear(tx *sql.Tx, groupJID, userJID, where string, at int64) (storedMemberLabel, bool, error) {
	var l storedMemberLabel
	var setAt, observedAt int64
	err := tx.QueryRow(`
		SELECT rowid, group_jid, user_jid, label, set_at, source, observed_at
		FROM group_member_labels
		WHERE group_jid = ? AND user_jid = ? AND `+where+`
		LIMIT 1
	`, groupJID, userJID, at).Scan(&l.rowID, &l.GroupJID, &l.UserJID, &l.Label, &setAt, &l.Source, &observedAt)
	if err == sql.ErrNoRows {
		return storedMemberLabel{}, false, nil
	}
	if err != nil {
		return storedMemberLabel{}, false, err
	}
	l.SetAt = fromUnix(setAt)
	l.ObservedAt = fromUnix(observedAt)
	return l, true, nil
}

// ListGroupMemberLabels returns the label history of a group, oldest first per
// member. An empty userJID lists every member.
func (d *DB) ListGroupMemberLabels(groupJID, userJID string) ([]GroupMemberLabel, error) {
	groupJID = strings.TrimSpace(groupJID)
	if groupJID == "" {
		return nil, fmt.Errorf("group JID is required")
	}
	if !d.memberLabelsEnabled {
		return []GroupMemberLabel{}, nil
	}
	query := `SELECT group_jid, user_jid, label, set_at, source, observed_at
		FROM group_member_labels WHERE group_jid = ?`
	args := []any{groupJID}
	if userJID = strings.TrimSpace(userJID); userJID != "" {
		query += ` AND user_jid = ?`
		args = append(args, userJID)
	}
	query += ` ORDER BY user_jid ASC, set_at ASC, rowid ASC`
	rows, err := d.readQuery().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupMemberLabel{}
	for rows.Next() {
		var l GroupMemberLabel
		var setAt, observedAt int64
		if err := rows.Scan(&l.GroupJID, &l.UserJID, &l.Label, &setAt, &l.Source, &observedAt); err != nil {
			return nil, err
		}
		l.SetAt = fromUnix(setAt)
		l.ObservedAt = fromUnix(observedAt)
		out = append(out, l)
	}
	return out, rows.Err()
}

// currentGroupMemberLabels maps each member of a group to their newest label.
func (d *DB) currentGroupMemberLabels(groupJID string) (map[string]GroupMemberLabel, error) {
	history, err := d.ListGroupMemberLabels(groupJID, "")
	if err != nil {
		return nil, err
	}
	current := make(map[string]GroupMemberLabel, len(history))
	for _, l := range history {
		current[l.UserJID] = l // history is ordered oldest first, so the last wins
	}
	return current, nil
}

func migrateLIDGroupMemberLabelsToPN(tx *sql.Tx, lidJID, pnJID string) error {
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO group_member_labels(group_jid, user_jid, label, set_at, source, observed_at)
		SELECT group_jid, ?, label, set_at, source, observed_at
		FROM group_member_labels
		WHERE user_jid = ?
	`, pnJID, lidJID); err != nil {
		return fmt.Errorf("merge lid group member labels: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM group_member_labels WHERE user_jid = ?`, lidJID); err != nil {
		return fmt.Errorf("delete lid group member labels: %w", err)
	}
	return nil
}
