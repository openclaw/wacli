package app

import (
	"database/sql"
	"errors"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

func markChatAsReadPosition(evt *events.MarkChatAsRead) (time.Time, []string) {
	r := evt.Action.GetMessageRange()
	ts := r.GetLastMessageTimestamp()
	var ids []string
	for _, m := range r.GetMessages() {
		if id := m.GetKey().GetID(); id != "" {
			ids = append(ids, id)
		}
		if m.GetTimestamp() > ts {
			ts = m.GetTimestamp()
		}
	}
	if ts > 0 {
		return time.Unix(ts, 0), ids
	}
	return evt.Timestamp, ids
}

func (a *App) receiptReadPosition(chat string, evt *events.Receipt) (time.Time, []string, error) {
	var through time.Time
	ids := make([]string, 0, len(evt.MessageIDs))
	for _, id := range evt.MessageIDs {
		ids = append(ids, string(id))
		m, err := a.db.GetMessage(chat, string(id))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return time.Time{}, nil, err
		}
		if m.Timestamp.After(through) {
			through = m.Timestamp
		}
	}
	// The receipt timestamp is a read-at time, not a message boundary. Use it
	// only when none of the named messages are available locally.
	if through.IsZero() {
		through = evt.Timestamp
	}
	return through, ids, nil
}
