package store

import (
	"fmt"
	"strings"
	"time"
)

// ClearChatUnreadThrough reduces the count without resurrecting messages that
// a later read already cleared. The update and recount share one SQLite snapshot.
func (d *DB) ClearChatUnreadThrough(jid string, through time.Time, ids []string) error {
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
	args = append(args, jid, jid)
	_, err := d.sql.ExecContext(storeCtx(), `
  WITH position AS (SELECT ? AS ts, `+boundary+` AS row_id),
  remaining AS (
   SELECT COUNT(*) AS count FROM messages, position
   WHERE chat_jid = ? AND from_me = 0
    AND COALESCE(reaction_to_id, '') = ''
    AND COALESCE(reaction_emoji, '') = ''
    AND revoked = 0 AND deleted_for_me = 0
    AND (COALESCE(display_text, '') != '(message)' OR TRIM(COALESCE(text, '')) != '')
    AND (messages.ts > position.ts OR (messages.ts = position.ts AND messages.rowid > position.row_id))
  )
  UPDATE chats SET unread_count = MIN(unread_count, remaining.count),
   unread = MIN(unread_count, remaining.count) > 0
  FROM remaining WHERE jid = ?`, args...)
	return err
}
