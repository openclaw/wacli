package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func migrateChatReadPositions(d *DB) error {
	hasChats, err := d.tableExists("chats")
	if err != nil || !hasChats {
		return err
	}
	for _, column := range []string{"read_through_ts", "read_through_rowid"} {
		exists, err := d.tableHasColumn("chats", column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := d.sql.Exec("ALTER TABLE chats ADD COLUMN " + column + " INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetHistoryChatUnread ignores stale snapshots once a local read position
// covers every stored incoming message. Apply after storing the conversation,
// so a newer inbound in the same snapshot can invalidate that evidence.
func (d *DB) SetHistoryChatUnread(jid string, count int, marked bool) error {
	count = max(count, 0)
	_, err := d.sql.ExecContext(storeCtx(), `
 INSERT INTO chats(jid, kind, unread, unread_count)
 SELECT ?, 'unknown', ?, ? WHERE NOT EXISTS (
  SELECT 1 FROM chats c WHERE c.jid = ? AND c.read_through_ts > 0
   AND NOT EXISTS (SELECT 1 FROM messages WHERE `+receiptEligible+`
    AND (ts > c.read_through_ts OR (ts = c.read_through_ts AND rowid > c.read_through_rowid)))
 ) ON CONFLICT(jid) DO UPDATE SET unread = excluded.unread,
 unread_count = CASE WHEN excluded.unread_count = 0 AND excluded.unread != 0
  THEN chats.unread_count ELSE excluded.unread_count END`,
		jid, count > 0 || marked, count, jid, jid)
	return err
}

// ClearChatUnreadThrough reduces the count without resurrecting messages that
// a later read already cleared. The update and recount share one SQLite snapshot.
func (d *DB) ClearChatUnreadThrough(jid string, through time.Time, ids []string) error {
	return d.clearChatUnreadThrough(jid, through, ids, false)
}

// ClearChatUnreadThroughPreservingMarker keeps a marker-only chat unread when
// readable messages remain beyond an explicit handled boundary. There is no
// count to reduce in that case; clearing the marker would acknowledge them all.
func (d *DB) ClearChatUnreadThroughPreservingMarker(jid string, through time.Time, ids []string) error {
	return d.clearChatUnreadThrough(jid, through, ids, true)
}

func (d *DB) clearChatUnreadThrough(jid string, through time.Time, ids []string, preserveMarker bool) error {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return fmt.Errorf("chat JID is required")
	}
	if through.IsZero() {
		return d.SetChatUnread(jid, false)
	}
	return clearChatUnreadThrough(d.sql, jid, through, ids, preserveMarker)
}

func clearChatUnreadThrough(db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, jid string, through time.Time, ids []string, preserveMarker bool) error {
	args := []any{unix(through)}
	boundary := "0"
	ids = uniqueNonEmptyStrings(ids)
	if len(ids) > 0 {
		boundary = "(SELECT COALESCE(MAX(rowid),0) FROM messages WHERE chat_jid = ? AND ts = ? AND msg_id IN (" + strings.TrimRight(strings.Repeat("?,", len(ids)), ",") + "))"
		args = append(args, jid, unix(through))
		for _, id := range ids {
			args = append(args, id)
		}
	}
	args = append(args, jid, preserveMarker, jid)
	_, err := db.ExecContext(storeCtx(), `
  WITH position AS (SELECT ? AS ts, `+boundary+` AS row_id),
  remaining AS (
   SELECT COUNT(*) AS count FROM messages, position
   WHERE chat_jid = ? AND from_me = 0
    AND COALESCE(reaction_to_id, '') = ''
    AND COALESCE(reaction_emoji, '') = ''
    AND revoked = 0 AND deleted_for_me = 0 AND deleted_at IS NULL
    AND (COALESCE(display_text, '') != '(message)' OR TRIM(COALESCE(text, '')) != '')
    AND (messages.ts > position.ts OR (messages.ts = position.ts AND messages.rowid > position.row_id))
  )
  UPDATE chats SET unread_count = MIN(unread_count, remaining.count),
   read_through_ts = MAX(read_through_ts, position.ts),
   read_through_rowid = CASE WHEN position.ts > read_through_ts THEN position.row_id
    WHEN position.ts = read_through_ts THEN MAX(read_through_rowid, position.row_id) ELSE read_through_rowid END,
   unread = CASE WHEN ? AND unread_count = 0 AND unread != 0 AND remaining.count > 0
    THEN 1 ELSE MIN(unread_count, remaining.count) > 0 END
  FROM remaining, position WHERE jid = ?`, args...)
	return err
}
