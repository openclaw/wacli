package store

import (
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
	q := `SELECT jid, COALESCE(name,''), COALESCE(owner_jid,''), COALESCE(created_ts,0), is_parent, COALESCE(linked_parent_jid,''), COALESCE(left_at,0), updated_at FROM groups WHERE left_at IS NULL`
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
		var created, left, updated int64
		var isParent int
		if err := rows.Scan(&g.JID, &g.Name, &g.OwnerJID, &created, &isParent, &g.LinkedParentJID, &left, &updated); err != nil {
			return nil, err
		}
		g.CreatedAt = fromUnix(created)
		g.IsParent = isParent != 0
		g.LeftAt = fromUnix(left)
		g.UpdatedAt = fromUnix(updated)
		out = append(out, g)
	}
	return out, rows.Err()
}
