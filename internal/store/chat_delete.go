package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChatDeleteRange is what the store holds of a chat that is to be deleted
// through a time, read in one snapshot across the chat's JIDs.
type ChatDeleteRange struct {
	// Known is whether the store has a chat row under any of them.
	Known bool
	// Last is the newest message at or before the time, reactions aside, which
	// the delete names as its last message; nil when there is none.
	Last *MessageInfo
	// Newer counts the messages later than the time, reactions and system
	// placeholders aside; NewestAt is the time of the newest of them.
	Newer    int
	NewestAt time.Time
}

// ChatDeleteRange reads the chat stored under jids (the same chat's phone and
// LID forms) for a delete through the given time. Store times are whole
// seconds, so a message stamped in through's second is at or before it.
func (d *DB) ChatDeleteRange(jids []string, through time.Time) (ChatDeleteRange, error) {
	if len(jids) == 0 {
		return ChatDeleteRange{}, fmt.Errorf("chat JID is required")
	}
	if through.IsZero() {
		return ChatDeleteRange{}, fmt.Errorf("through time is required")
	}
	cut := through.UTC().Unix()
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(jids)), ",") + ")"
	args := make([]any, len(jids))
	for i, jid := range jids {
		args[i] = jid
	}

	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ChatDeleteRange{}, err
	}
	defer tx.Rollback()

	var r ChatDeleteRange
	// A message's chat row comes first (foreign key), so the rows say it all.
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM chats WHERE jid IN "+in+")", args...).Scan(&r.Known); err != nil {
		return ChatDeleteRange{}, fmt.Errorf("look up chat: %w", err)
	}
	if !r.Known {
		return r, tx.Commit()
	}

	var last MessageInfo
	var ts int64
	var fromMe int
	err = tx.QueryRow(
		"SELECT chat_jid, msg_id, ts, from_me, COALESCE(sender_jid,''), COALESCE(sender_name,'') FROM messages WHERE chat_jid IN "+in+
			" AND "+notReaction+" AND ts > 0 AND ts <= ? ORDER BY ts DESC, rowid DESC LIMIT 1",
		append(append([]any{}, args...), cut)...,
	).Scan(&last.ChatJID, &last.MsgID, &ts, &fromMe, &last.SenderJID, &last.SenderName)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return ChatDeleteRange{}, fmt.Errorf("find last message: %w", err)
	default:
		last.Timestamp = fromUnix(ts)
		last.FromMe = fromMe != 0
		r.Last = &last
	}

	var newest int64
	if err := tx.QueryRow(
		"SELECT COUNT(*), COALESCE(MAX(ts),0) FROM messages WHERE chat_jid IN "+in+
			" AND "+notReaction+" AND "+notPlaceholder+" AND ts > ?",
		append(append([]any{}, args...), cut)...,
	).Scan(&r.Newer, &newest); err != nil {
		return ChatDeleteRange{}, fmt.Errorf("count newer messages: %w", err)
	}
	r.NewestAt = fromUnix(newest)
	return r, tx.Commit()
}
