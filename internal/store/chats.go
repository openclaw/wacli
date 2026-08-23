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

func (d *DB) UpsertChatMetadata(jid, kind, name string) error {
	if strings.TrimSpace(kind) == "" {
		kind = "unknown"
	}
	return d.q.UpsertChatMetadata(storeCtx(), storedb.UpsertChatMetadataParams{
		Jid:  jid,
		Kind: kind,
		Name: nullString(name),
	})
}

// UpsertHistoryChat records lightweight conversation state even when its
// messages are intentionally excluded from the local index.
func (d *DB) UpsertHistoryChat(jid, kind, name string, lastTS time.Time, archived *bool) error {
	if strings.TrimSpace(kind) == "" {
		kind = "unknown"
	}
	archivedValue := any(nil)
	if archived != nil {
		archivedValue = boolToInt(*archived)
	}
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO chats(jid,kind,name,last_message_ts,archived)
		VALUES(?,?,?,?,COALESCE(?,0))
		ON CONFLICT(jid) DO UPDATE SET
		kind=excluded.kind,
		name=CASE WHEN excluded.name IS NOT NULL AND excluded.name != '' THEN excluded.name ELSE chats.name END,
		last_message_ts=MAX(COALESCE(chats.last_message_ts,0),COALESCE(excluded.last_message_ts,0)),
		archived=COALESCE(?, chats.archived)`,
		jid, kind, nullString(name), sqlNullInt64(unix(lastTS)), archivedValue, archivedValue)
	return err
}

// ApplySyncOptimizationRetention prunes indexed payloads while keeping chat,
// contact and group metadata available for recipient resolution.
func (d *DB) ApplySyncOptimizationRetention(p SyncOptimizationPolicy) (int64, error) {
	if !p.Enabled {
		return 0, nil
	}
	if err := p.Validate(); err != nil {
		return 0, err
	}
	q := `SELECT jid FROM chats`
	if p.ExcludeArchived {
		q += ` WHERE archived=0`
	}
	q += ` ORDER BY COALESCE(last_message_ts,0) DESC, jid ASC LIMIT ?`
	rows, err := d.sql.QueryContext(storeCtx(), q, p.MaxChats)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	keep := make(map[string]struct{}, p.MaxChats)
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return 0, err
		}
		keep[jid] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	indexed, err := d.sql.QueryContext(storeCtx(), `SELECT DISTINCT chat_jid FROM messages`)
	if err != nil {
		return 0, err
	}
	defer indexed.Close()
	var pruned int64
	for indexed.Next() {
		var jid string
		if err := indexed.Scan(&jid); err != nil {
			return pruned, err
		}
		if _, ok := keep[jid]; !ok && p.PruneEvictedChats {
			n, err := d.PruneIndexedChatData(jid)
			if err != nil {
				return pruned, err
			}
			pruned += n
		}
	}
	if err := indexed.Err(); err != nil {
		return pruned, err
	}
	for jid := range keep {
		n, err := d.PruneChatMessagesToLimit(jid, p.MaxMessagesPerChat)
		if err != nil {
			return pruned, err
		}
		pruned += n
	}
	if !p.PersistCalls {
		if _, err := d.sql.ExecContext(storeCtx(), `DELETE FROM call_events`); err != nil {
			return pruned, err
		}
	}
	if !p.PersistStatuses {
		if _, err := d.sql.ExecContext(storeCtx(), `DELETE FROM status_messages`); err != nil {
			return pruned, err
		}
	}
	return pruned, nil
}

func (d *DB) ChatIsInSyncOptimizationSet(jid string, p SyncOptimizationPolicy) (bool, error) {
	if !p.Enabled {
		return true, nil
	}
	q := `SELECT jid FROM chats`
	if p.ExcludeArchived {
		q += ` WHERE archived=0`
	}
	q += ` ORDER BY COALESCE(last_message_ts,0) DESC, jid ASC LIMIT ?`
	rows, err := d.sql.QueryContext(storeCtx(), q, p.MaxChats)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate string
		if err := rows.Scan(&candidate); err != nil {
			return false, err
		}
		if candidate == jid {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (d *DB) PruneIndexedChatData(jid string) (int64, error) {
	tx, err := d.sql.BeginTx(storeCtx(), nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM poll_votes WHERE chat_jid=?`, jid); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM polls WHERE chat_jid=?`, jid); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM message_locations WHERE chat_jid=?`, jid); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM starred WHERE chat_jid=?`, jid); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM call_events WHERE chat_jid=?`, jid); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM messages WHERE chat_jid=?`, jid)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (d *DB) PruneChatMessagesToLimit(jid string, limit int) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("message limit must be positive")
	}
	res, err := d.sql.ExecContext(storeCtx(), `DELETE FROM messages WHERE chat_jid=? AND rowid NOT IN (SELECT rowid FROM messages WHERE chat_jid=? ORDER BY ts DESC,rowid DESC LIMIT ?)`, jid, jid, limit)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// Remove records keyed to messages that were just evicted.
	_, _ = d.sql.ExecContext(storeCtx(), `DELETE FROM message_locations WHERE chat_jid=? AND msg_id NOT IN (SELECT msg_id FROM messages WHERE chat_jid=?)`, jid, jid)
	_, _ = d.sql.ExecContext(storeCtx(), `DELETE FROM starred WHERE chat_jid=? AND msg_id NOT IN (SELECT msg_id FROM messages WHERE chat_jid=?)`, jid, jid)
	_, _ = d.sql.ExecContext(storeCtx(), `DELETE FROM polls WHERE chat_jid=? AND msg_id NOT IN (SELECT msg_id FROM messages WHERE chat_jid=?)`, jid, jid)
	_, _ = d.sql.ExecContext(storeCtx(), `DELETE FROM poll_votes WHERE chat_jid=? AND poll_msg_id NOT IN (SELECT msg_id FROM messages WHERE chat_jid=?)`, jid, jid)
	return n, nil
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
	if err := q.DeleteMessageLocationsForChat(ctx, jid); err != nil {
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
	if _, err := tx.Exec(`DELETE FROM message_locations WHERE chat_jid IN (`+staleChatJIDsSQL+`)`, cutoffUnix); err != nil {
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
