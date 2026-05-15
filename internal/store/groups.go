package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store/storedb"
)

func (d *DB) UpsertGroup(jid, name, ownerJID string, created time.Time) error {
	return d.q.UpsertGroup(storeCtx(), storedb.UpsertGroupParams{
		Jid:       jid,
		Name:      nullString(name),
		OwnerJid:  nullString(ownerJID),
		CreatedTs: sqlNullInt64(unix(created)),
		UpdatedAt: nowUTC().Unix(),
	})
}

func (d *DB) UpsertGroupWithHierarchy(jid, name, ownerJID string, created time.Time, isParent bool, linkedParentJID string) error {
	linkedParentJID = strings.TrimSpace(linkedParentJID)
	if isParent {
		linkedParentJID = ""
	}
	return d.q.UpsertGroupWithHierarchy(storeCtx(), storedb.UpsertGroupWithHierarchyParams{
		Jid:             jid,
		Name:            nullString(name),
		OwnerJid:        nullString(ownerJID),
		CreatedTs:       sqlNullInt64(unix(created)),
		IsParent:        boolToInt64(isParent),
		LinkedParentJid: nullString(linkedParentJID),
		UpdatedAt:       nowUTC().Unix(),
	})
}

func (d *DB) MarkGroupLeft(jid string, leftAt time.Time) error {
	if leftAt.IsZero() {
		leftAt = nowUTC()
	}
	return d.q.MarkGroupLeft(storeCtx(), storedb.MarkGroupLeftParams{
		LeftAt:    sqlNullInt64(unix(leftAt)),
		UpdatedAt: nowUTC().Unix(),
		Jid:       jid,
	})
}

func (d *DB) MarkGroupsMissingFrom(joined map[string]bool, leftAt time.Time) error {
	if leftAt.IsZero() {
		leftAt = nowUTC()
	}
	joinedJIDs, err := d.q.ListJoinedGroupJIDs(storeCtx())
	if err != nil {
		return err
	}
	var missing []string
	for _, jid := range joinedJIDs {
		if !joined[jid] {
			missing = append(missing, jid)
		}
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

	q := d.q.WithTx(tx)
	ctx := storeCtx()
	if err = q.DeleteGroupParticipants(ctx, groupJID); err != nil {
		return err
	}
	now := nowUTC()
	for _, participant := range participants {
		role := strings.TrimSpace(participant.Role)
		if role == "" {
			role = "member"
		}
		if err = q.InsertGroupParticipant(ctx, storedb.InsertGroupParticipantParams{
			GroupJid:  groupJID,
			UserJid:   participant.UserJID,
			Role:      sql.NullString{String: role, Valid: true},
			UpdatedAt: unix(now),
		}); err != nil {
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
	return d.q.DeleteGroup(storeCtx(), jid)
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
	q := d.q.WithTx(tx)
	ctx := storeCtx()
	if err = q.DeleteGroup(ctx, jid); err != nil {
		return err
	}
	if err = q.DeletePollVotesForChat(ctx, jid); err != nil {
		return err
	}
	if err = q.DeletePollsForChat(ctx, jid); err != nil {
		return err
	}
	if err = q.DeleteStarredForChat(ctx, jid); err != nil {
		return err
	}
	if err = q.DeleteChat(ctx, jid); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) ListLeftGroups() ([]Group, error) {
	rows, err := d.q.ListLeftGroups(storeCtx())
	if err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(rows))
	for _, row := range rows {
		out = append(out, groupFromLeftRow(row))
	}
	return out, nil
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
	return d.q.DeleteLeftGroups(storeCtx())
}

func (d *DB) DeleteLeftGroupsOlderThan(days int) (int64, error) {
	if days <= 0 {
		return 0, fmt.Errorf("days must be positive")
	}
	cutoff := nowUTC().AddDate(0, 0, -days)
	return d.q.DeleteLeftGroupsOlderThan(storeCtx(), sqlNullInt64(unix(cutoff)))
}

func groupFromLeftRow(row storedb.ListLeftGroupsRow) Group {
	return Group{
		JID:             row.Jid,
		Name:            row.Name,
		OwnerJID:        row.OwnerJid,
		IsParent:        row.IsParent != 0,
		LinkedParentJID: row.LinkedParentJid,
		CreatedAt:       fromUnix(row.CreatedTs),
		LeftAt:          fromUnix(row.LeftAt),
		UpdatedAt:       fromUnix(row.UpdatedAt),
	}
}
