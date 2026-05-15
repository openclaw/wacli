package store

import (
	"fmt"
	"strings"
	"time"
)

type ChatListFilter struct {
	Query    string
	Limit    int
	Archived *bool
	Pinned   *bool
	Muted    *bool
	Unread   *bool
}

func (d *DB) UpsertChat(jid, kind, name string, lastTS time.Time) error {
	if strings.TrimSpace(kind) == "" {
		kind = "unknown"
	}
	_, err := d.sql.Exec(`
		INSERT INTO chats(jid, kind, name, last_message_ts)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			kind=excluded.kind,
			name=CASE WHEN excluded.name IS NOT NULL AND excluded.name != '' THEN excluded.name ELSE chats.name END,
			last_message_ts=CASE WHEN excluded.last_message_ts > COALESCE(chats.last_message_ts, 0) THEN excluded.last_message_ts ELSE chats.last_message_ts END
	`, jid, kind, name, unix(lastTS))
	return err
}

func (d *DB) ListChats(query string, limit int) ([]Chat, error) {
	return d.ListChatsFiltered(ChatListFilter{Query: query, Limit: limit})
}

func (d *DB) ListChatsFiltered(f ChatListFilter) ([]Chat, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q := `SELECT jid, kind, COALESCE(name,''), COALESCE(last_message_ts,0), COALESCE(archived,0), COALESCE(pinned,0), COALESCE(muted_until,0), COALESCE(unread,0) FROM chats WHERE 1=1`
	var args []interface{}
	if strings.TrimSpace(f.Query) != "" {
		q += ` AND (LOWER(name) LIKE LOWER(?) ESCAPE '\' OR LOWER(jid) LIKE LOWER(?) ESCAPE '\')`
		needle := likeContains(f.Query)
		args = append(args, needle, needle)
	}
	if f.Archived != nil {
		q += ` AND archived = ?`
		args = append(args, boolToInt(*f.Archived))
	}
	if f.Pinned != nil {
		q += ` AND pinned = ?`
		args = append(args, boolToInt(*f.Pinned))
	}
	if f.Muted != nil {
		now := nowUTC().Unix()
		if *f.Muted {
			q += ` AND (muted_until = -1 OR muted_until > ?)`
		} else {
			q += ` AND (muted_until = 0 OR (muted_until > 0 AND muted_until <= ?))`
		}
		args = append(args, now)
	}
	if f.Unread != nil {
		q += ` AND unread = ?`
		args = append(args, boolToInt(*f.Unread))
	}
	q += ` ORDER BY pinned DESC, last_message_ts DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		var archived, pinned, unread int
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &ts, &archived, &pinned, &c.MutedUntil, &unread); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(ts)
		c.Archived = archived != 0
		c.Pinned = pinned != 0
		c.Unread = unread != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) GetChat(jid string) (Chat, error) {
	row := d.sql.QueryRow(`SELECT jid, kind, COALESCE(name,''), COALESCE(last_message_ts,0), COALESCE(archived,0), COALESCE(pinned,0), COALESCE(muted_until,0), COALESCE(unread,0) FROM chats WHERE jid = ?`, jid)
	var c Chat
	var ts int64
	var archived, pinned, unread int
	if err := row.Scan(&c.JID, &c.Kind, &c.Name, &ts, &archived, &pinned, &c.MutedUntil, &unread); err != nil {
		return Chat{}, err
	}
	c.LastMessageTS = fromUnix(ts)
	c.Archived = archived != 0
	c.Pinned = pinned != 0
	c.Unread = unread != 0
	return c, nil
}

func (d *DB) SetChatArchived(jid string, archived bool) error {
	pinned := ""
	if archived {
		pinned = ", pinned = 0"
	}
	_, err := d.sql.Exec(`
		INSERT INTO chats(jid, kind, archived) VALUES(?, 'unknown', ?)
		ON CONFLICT(jid) DO UPDATE SET archived=excluded.archived`+pinned,
		jid, boolToInt(archived),
	)
	return err
}

func (d *DB) SetChatPinned(jid string, pinned bool) error {
	_, err := d.sql.Exec(`
		INSERT INTO chats(jid, kind, pinned) VALUES(?, 'unknown', ?)
		ON CONFLICT(jid) DO UPDATE SET pinned=excluded.pinned
	`, jid, boolToInt(pinned))
	return err
}

func (d *DB) SetChatMutedUntil(jid string, mutedUntil int64) error {
	_, err := d.sql.Exec(`
		INSERT INTO chats(jid, kind, muted_until) VALUES(?, 'unknown', ?)
		ON CONFLICT(jid) DO UPDATE SET muted_until=excluded.muted_until
	`, jid, mutedUntil)
	return err
}

func (d *DB) SetChatUnread(jid string, unread bool) error {
	_, err := d.sql.Exec(`
		INSERT INTO chats(jid, kind, unread) VALUES(?, 'unknown', ?)
		ON CONFLICT(jid) DO UPDATE SET unread=excluded.unread
	`, jid, boolToInt(unread))
	return err
}

func (d *DB) DeleteChat(jid string) error {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return fmt.Errorf("chat JID is required")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(`DELETE FROM poll_votes WHERE chat_jid = ?`, jid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM polls WHERE chat_jid = ?`, jid); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chats WHERE jid = ?`, jid); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

const staleChatJIDsSQL = `
	SELECT jid FROM (
		SELECT
			c.jid,
			CASE
				WHEN COALESCE(MAX(m.ts), 0) > COALESCE(c.last_message_ts, 0) THEN COALESCE(MAX(m.ts), 0)
				ELSE COALESCE(c.last_message_ts, 0)
			END AS activity_ts
		FROM chats c
		LEFT JOIN messages m ON m.chat_jid = c.jid
		GROUP BY c.jid
	)
	WHERE activity_ts > 0 AND activity_ts < ?
`

func (d *DB) DeleteChatsOlderThan(days int) (int64, error) {
	if days <= 0 {
		return 0, fmt.Errorf("days must be positive")
	}
	cutoff := nowUTC().AddDate(0, 0, -days)
	cutoffUnix := unix(cutoff)
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(`DELETE FROM poll_votes WHERE chat_jid IN (`+staleChatJIDsSQL+`)`, cutoffUnix); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM polls WHERE chat_jid IN (`+staleChatJIDsSQL+`)`, cutoffUnix); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM chats WHERE jid IN (`+staleChatJIDsSQL+`)`, cutoffUnix)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	tx = nil
	return res.RowsAffected()
}

func (d *DB) ListChatsOlderThan(days int) ([]Chat, error) {
	if days <= 0 {
		return nil, fmt.Errorf("days must be positive")
	}
	cutoff := nowUTC().AddDate(0, 0, -days)
	rows, err := d.sql.Query(`
		SELECT jid, kind, name, last_message_ts, archived, pinned, muted_until, unread
		FROM (
			SELECT
				c.jid,
				c.kind,
				COALESCE(c.name,'') AS name,
				COALESCE(c.last_message_ts,0) AS last_message_ts,
				COALESCE(c.archived,0) AS archived,
				COALESCE(c.pinned,0) AS pinned,
				COALESCE(c.muted_until,0) AS muted_until,
				COALESCE(c.unread,0) AS unread,
				CASE
					WHEN COALESCE(MAX(m.ts), 0) > COALESCE(c.last_message_ts, 0) THEN COALESCE(MAX(m.ts), 0)
					ELSE COALESCE(c.last_message_ts, 0)
				END AS activity_ts
			FROM chats c
			LEFT JOIN messages m ON m.chat_jid = c.jid
			GROUP BY c.jid
		)
		WHERE activity_ts > 0 AND activity_ts < ?
		ORDER BY activity_ts ASC
	`, unix(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Chat
	for rows.Next() {
		var c Chat
		var ts int64
		var archived, pinned, unread int
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &ts, &archived, &pinned, &c.MutedUntil, &unread); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(ts)
		c.Archived = archived != 0
		c.Pinned = pinned != 0
		c.Unread = unread != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) CountChatMessages(jid string) (int64, error) {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return 0, fmt.Errorf("chat JID is required")
	}
	row := d.sql.QueryRow(`SELECT COUNT(1) FROM messages WHERE chat_jid = ?`, jid)
	var n int64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
