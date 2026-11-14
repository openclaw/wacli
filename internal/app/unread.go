package app

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// Exact message ranges describe a handled prefix, including when its echo is
// persisted before local completion or replayed after restart. Their events do
// not carry the originating CLI flags, so preserve an existing marker whenever
// newer readable rows remain. Timestamp-only events keep their legacy behavior.
func (a *App) clearChatUnreadFromEvent(chat string, through time.Time, ids []string, exact bool) error {
	preserve := false
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			preserve = true
			break
		}
	}
	if !exact {
		return a.db.ClearChatUnreadThroughApproximate(chat, through, preserve)
	}
	if preserve {
		return a.db.ClearChatUnreadThroughPreservingMarker(chat, through, ids)
	}
	return a.db.ClearChatUnreadThrough(chat, through, ids)
}

func markChatAsReadPosition(evt *events.MarkChatAsRead) (time.Time, []string, bool) {
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
		return time.Unix(ts, 0), ids, true
	}
	return evt.Timestamp, ids, false
}

func (a *App) receiptReadPosition(chat string, evt *events.Receipt) (time.Time, []string, bool, error) {
	var through time.Time
	ids := make([]string, 0, len(evt.MessageIDs))
	for _, id := range evt.MessageIDs {
		ids = append(ids, string(id))
		m, err := a.db.GetMessage(chat, string(id))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return time.Time{}, nil, false, err
		}
		if m.Timestamp.After(through) {
			through = m.Timestamp
		}
	}
	// The receipt timestamp is a read-at time, not a message boundary. Use it
	// only when none of the named messages are available locally, and never
	// as durable read evidence.
	if through.IsZero() {
		return evt.Timestamp, ids, false, nil
	}
	return through, ids, true, nil
}
