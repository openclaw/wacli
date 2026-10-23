package store

import (
	"fmt"
	"strings"
	"time"
)

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
	_, err := d.sql.ExecContext(storeCtx(), `
  WITH position AS (SELECT ? AS ts, `+boundary+` AS row_id),
  remaining AS (
   SELECT COUNT(*) AS count FROM messages, position
   WHERE chat_jid = ? AND from_me = 0
    AND `+notReaction+`
    AND revoked = 0 AND deleted_for_me = 0
    AND `+notPlaceholder+`
    AND (messages.ts > position.ts OR (messages.ts = position.ts AND messages.rowid > position.row_id))
  )
  UPDATE chats SET unread_count = MIN(unread_count, remaining.count),
   unread = CASE WHEN ? AND unread_count = 0 AND unread != 0 AND remaining.count > 0
    THEN 1 ELSE MIN(unread_count, remaining.count) > 0 END
  FROM remaining WHERE jid = ?`, args...)
	return err
}
