package store

import (
	"fmt"
	"strings"
	"time"
)

func (d *DB) UpsertGroup(jid, name, ownerJID string, created time.Time) error {
	now := nowUTC().Unix()
	_, err := d.sql.Exec(`
		INSERT INTO groups(jid, name, owner_jid, created_ts, left_at, updated_at)
		VALUES (?, ?, ?, ?, NULL, ?)
		ON CONFLICT(jid) DO UPDATE SET
			name=COALESCE(NULLIF(excluded.name,''), groups.name),
			owner_jid=COALESCE(NULLIF(excluded.owner_jid,''), groups.owner_jid),
			created_ts=COALESCE(NULLIF(excluded.created_ts,0), groups.created_ts),
			left_at=NULL,
			updated_at=excluded.updated_at
	`, jid, name, ownerJID, unix(created), now)
	return err
}

func (d *DB) UpsertGroupWithHierarchy(jid, name, ownerJID string, created time.Time, isParent bool, linkedParentJID string) error {
	now := nowUTC().Unix()
	linkedParentJID = strings.TrimSpace(linkedParentJID)
	if isParent {
		linkedParentJID = ""
	}
	_, err := d.sql.Exec(`
		INSERT INTO groups(jid, name, owner_jid, created_ts, is_parent, linked_parent_jid, left_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, ?)
		ON CONFLICT(jid) DO UPDATE SET
			name=COALESCE(NULLIF(excluded.name,''), groups.name),
			owner_jid=COALESCE(NULLIF(excluded.owner_jid,''), groups.owner_jid),
			created_ts=COALESCE(NULLIF(excluded.created_ts,0), groups.created_ts),
			is_parent=excluded.is_parent,
			linked_parent_jid=excluded.linked_parent_jid,
			left_at=NULL,
			updated_at=excluded.updated_at
	`, jid, name, ownerJID, unix(created), boolToInt(isParent), nullIfEmpty(linkedParentJID), now)
	return err
}

func (d *DB) MarkGroupLeft(jid string, leftAt time.Time) error {
	now := nowUTC().Unix()
	if leftAt.IsZero() {
		leftAt = nowUTC()
	}
	_, err := d.sql.Exec(`
		UPDATE groups
		SET left_at = ?, updated_at = ?
		WHERE jid = ?
	`, unix(leftAt), now, jid)
	return err
}

func (d *DB) MarkGroupsMissingFrom(joined map[string]bool, leftAt time.Time) error {
	if leftAt.IsZero() {
		leftAt = nowUTC()
	}
	rows, err := d.sql.Query(`SELECT jid FROM groups WHERE left_at IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var missing []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return err
		}
		if !joined[jid] {
			missing = append(missing, jid)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, jid := range missing {
		if err := d.MarkGroupLeft(jid, leftAt); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) ReplaceGroupParticipants(groupJID string, participants []GroupParticipant) (err error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM group_participants WHERE group_jid = ?`, groupJID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO group_participants(group_jid, user_jid, role, updated_at) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := nowUTC()
	for _, participant := range participants {
		role := strings.TrimSpace(participant.Role)
		if role == "" {
			role = "member"
		}
		if _, err = stmt.Exec(groupJID, participant.UserJID, role, unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) ListGroups(query string, limit int) ([]Group, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT jid, COALESCE(name,''), COALESCE(owner_jid,''), is_parent, COALESCE(linked_parent_jid,''), COALESCE(created_ts,0), COALESCE(left_at,0), updated_at FROM groups WHERE left_at IS NULL`
	var args []interface{}
	if strings.TrimSpace(query) != "" {
		needle := likeContains(query)
		q += ` AND (LOWER(name) LIKE LOWER(?) ESCAPE '\' OR LOWER(jid) LIKE LOWER(?) ESCAPE '\')`
		args = append(args, needle, needle)
	}
	q += ` ORDER BY COALESCE(created_ts,0) DESC LIMIT ?`
	args = append(args, limit)

	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		var isParent int
		var created, left, updated int64
		if err := rows.Scan(&g.JID, &g.Name, &g.OwnerJID, &isParent, &g.LinkedParentJID, &created, &left, &updated); err != nil {
			return nil, err
		}
		g.IsParent = isParent != 0
		g.CreatedAt = fromUnix(created)
		g.LeftAt = fromUnix(left)
		g.UpdatedAt = fromUnix(updated)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (d *DB) DeleteGroup(jid string) error {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return fmt.Errorf("group JID is required")
	}
	_, err := d.sql.Exec(`DELETE FROM groups WHERE jid = ?`, jid)
	return err
}

func (d *DB) DeleteGroupLocalData(jid string) (err error) {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return fmt.Errorf("group JID is required")
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
	if _, err = tx.Exec(`DELETE FROM groups WHERE jid = ?`, jid); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM poll_votes WHERE chat_jid = ?`, jid); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM polls WHERE chat_jid = ?`, jid); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM chats WHERE jid = ?`, jid); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) ListLeftGroups() ([]Group, error) {
	rows, err := d.sql.Query(`
		SELECT jid, COALESCE(name,''), COALESCE(owner_jid,''), is_parent, COALESCE(linked_parent_jid,''), COALESCE(created_ts,0), COALESCE(left_at,0), updated_at
		FROM groups
		WHERE left_at IS NOT NULL
		ORDER BY left_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		var isParent int
		var created, left, updated int64
		if err := rows.Scan(&g.JID, &g.Name, &g.OwnerJID, &isParent, &g.LinkedParentJID, &created, &left, &updated); err != nil {
			return nil, err
		}
		g.IsParent = isParent != 0
		g.CreatedAt = fromUnix(created)
		g.LeftAt = fromUnix(left)
		g.UpdatedAt = fromUnix(updated)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (d *DB) ListPrunableGroups(days int, includeActive bool) ([]Group, error) {
	if days < 0 {
		return nil, fmt.Errorf("days must not be negative")
	}
	if includeActive && days <= 0 {
		return nil, fmt.Errorf("days must be positive when pruning active groups")
	}
	cutoff := int64(0)
	if days > 0 {
		cutoff = unix(nowUTC().AddDate(0, 0, -days))
	}
	rows, err := d.sql.Query(`
		SELECT jid, name, owner_jid, is_parent, linked_parent_jid, created_ts, left_at, updated_at
		FROM (
			SELECT
				g.jid,
				COALESCE(g.name,'') AS name,
				COALESCE(g.owner_jid,'') AS owner_jid,
				g.is_parent,
				COALESCE(g.linked_parent_jid,'') AS linked_parent_jid,
				COALESCE(g.created_ts,0) AS created_ts,
				COALESCE(g.left_at,0) AS left_at,
				g.updated_at,
				CASE
					WHEN COALESCE(MAX(m.ts), 0) > COALESCE(c.last_message_ts, 0) THEN COALESCE(MAX(m.ts), 0)
					ELSE COALESCE(c.last_message_ts, 0)
				END AS activity_ts
			FROM groups g
			LEFT JOIN chats c ON c.jid = g.jid
			LEFT JOIN messages m ON m.chat_jid = g.jid
			GROUP BY g.jid
		)
		WHERE
			(left_at > 0 AND (? = 0 OR left_at < ?))
			OR (? = 1 AND left_at = 0 AND activity_ts > 0 AND activity_ts < ?)
		ORDER BY CASE WHEN left_at > 0 THEN left_at ELSE activity_ts END ASC
	`, cutoff, cutoff, boolToInt(includeActive), cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Group
	for rows.Next() {
		var g Group
		var isParent int
		var created, left, updated int64
		if err := rows.Scan(&g.JID, &g.Name, &g.OwnerJID, &isParent, &g.LinkedParentJID, &created, &left, &updated); err != nil {
			return nil, err
		}
		g.IsParent = isParent != 0
		g.CreatedAt = fromUnix(created)
		g.LeftAt = fromUnix(left)
		g.UpdatedAt = fromUnix(updated)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (d *DB) DeleteLeftGroups() (int64, error) {
	res, err := d.sql.Exec(`DELETE FROM groups WHERE left_at IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) DeleteLeftGroupsOlderThan(days int) (int64, error) {
	if days <= 0 {
		return 0, fmt.Errorf("days must be positive")
	}
	cutoff := nowUTC().AddDate(0, 0, -days)
	res, err := d.sql.Exec(`DELETE FROM groups WHERE left_at IS NOT NULL AND left_at < ?`, unix(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
