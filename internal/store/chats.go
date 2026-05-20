package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store/storedb"
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
	return d.q.UpsertChat(storeCtx(), storedb.UpsertChatParams{
		Jid:           jid,
		Kind:          kind,
		Name:          nullString(name),
		LastMessageTs: sqlNullInt64(unix(lastTS)),
	})
}

func (d *DB) ListChats(query string, limit int) ([]Chat, error) {
	return d.ListChatsFiltered(ChatListFilter{Query: query, Limit: limit})
}

func (d *DB) CountChats() (int64, error) {
	return d.q.CountChats(storeCtx())
}

func (d *DB) ListChatsFiltered(f ChatListFilter) ([]Chat, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q := `SELECT jid, kind, COALESCE(name,''), COALESCE(last_message_ts,0), COALESCE(archived,0), COALESCE(pinned,0), COALESCE(muted_until,0), COALESCE(unread,0), COALESCE(unread_count,0) FROM chats WHERE 1=1`
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
		if *f.Unread {
			q += ` AND unread != 0`
		} else {
			q += ` AND unread = 0`
		}
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
		var archived, pinned, unread, unreadCount int
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &ts, &archived, &pinned, &c.MutedUntil, &unread, &unreadCount); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(ts)
		c.Archived = archived != 0
		c.Pinned = pinned != 0
		applyChatUnread(&c, unread, unreadCount)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) GetChat(jid string) (Chat, error) {
	row, err := d.q.GetChat(storeCtx(), jid)
	if err != nil {
		return Chat{}, err
	}
	return chatFromRow(row), nil
}

func (d *DB) SetChatArchived(jid string, archived bool) error {
	if archived {
		return d.q.SetChatArchivedAndUnpin(storeCtx(), storedb.SetChatArchivedAndUnpinParams{Jid: jid, Archived: boolToInt64(archived)})
	}
	return d.q.SetChatArchived(storeCtx(), storedb.SetChatArchivedParams{Jid: jid, Archived: boolToInt64(archived)})
}

func (d *DB) SetChatPinned(jid string, pinned bool) error {
	return d.q.SetChatPinned(storeCtx(), storedb.SetChatPinnedParams{Jid: jid, Pinned: boolToInt64(pinned)})
}

func (d *DB) SetChatMutedUntil(jid string, mutedUntil int64) error {
	return d.q.SetChatMutedUntil(storeCtx(), storedb.SetChatMutedUntilParams{Jid: jid, MutedUntil: mutedUntil})
}

func (d *DB) SetChatUnread(jid string, unread bool) error {
	if unread {
		_, err := d.sql.ExecContext(storeCtx(), `
			INSERT INTO chats(jid, kind, unread)
			VALUES(?, 'unknown', 1)
			ON CONFLICT(jid) DO UPDATE SET unread=1
		`, jid)
		return err
	}
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO chats(jid, kind, unread, unread_count)
		VALUES(?, 'unknown', 0, 0)
		ON CONFLICT(jid) DO UPDATE SET unread=0, unread_count=0
	`, jid)
	return err
}

func (d *DB) SetChatUnreadCount(jid string, count int) error {
	if count < 0 {
		count = 0
	}
	unread := 0
	if count > 0 {
		unread = 1
	}
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO chats(jid, kind, unread, unread_count)
		VALUES(?, 'unknown', ?, ?)
		ON CONFLICT(jid) DO UPDATE SET unread=excluded.unread, unread_count=excluded.unread_count
	`, jid, unread, count)
	return err
}

func (d *DB) IncrementChatUnread(jid string) error {
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO chats(jid, kind, unread, unread_count)
		VALUES(?, 'unknown', 1, 1)
		ON CONFLICT(jid) DO UPDATE SET
			unread=1,
			unread_count=COALESCE(chats.unread_count, 0) + 1
	`, jid)
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
	q := d.q.WithTx(tx)
	ctx := storeCtx()
	if err := q.DeletePollVotesForChat(ctx, jid); err != nil {
		return err
	}
	if err := q.DeletePollsForChat(ctx, jid); err != nil {
		return err
	}
	if err := q.DeleteStarredForChat(ctx, jid); err != nil {
		return err
	}
	if err := q.DeleteChat(ctx, jid); err != nil {
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
	if _, err := tx.Exec(`DELETE FROM starred WHERE chat_jid IN (`+staleChatJIDsSQL+`)`, cutoffUnix); err != nil {
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
		SELECT jid, kind, name, last_message_ts, archived, pinned, muted_until, unread, unread_count
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
				COALESCE(c.unread_count,0) AS unread_count,
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
		var archived, pinned, unread, unreadCount int
		if err := rows.Scan(&c.JID, &c.Kind, &c.Name, &ts, &archived, &pinned, &c.MutedUntil, &unread, &unreadCount); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(ts)
		c.Archived = archived != 0
		c.Pinned = pinned != 0
		applyChatUnread(&c, unread, unreadCount)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) CountChatMessages(jid string) (int64, error) {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return 0, fmt.Errorf("chat JID is required")
	}
	return d.q.CountChatMessages(storeCtx(), jid)
}

func chatFromRow(row storedb.GetChatRow) Chat {
	c := Chat{
		JID:           row.Jid,
		Kind:          row.Kind,
		Name:          row.Name,
		LastMessageTS: fromUnix(row.LastMessageTs),
		Archived:      row.Archived != 0,
		Pinned:        row.Pinned != 0,
		MutedUntil:    row.MutedUntil,
	}
	applyChatUnread(&c, int(row.Unread), int(row.UnreadCount))
	return c
}

func applyChatUnread(c *Chat, unread, unreadCount int) {
	c.Unread = unread != 0
	if unreadCount > 0 {
		c.UnreadCount = unreadCount
	} else {
		c.UnreadCount = 0
	}
}

func sqlNullInt64(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: true}
}
