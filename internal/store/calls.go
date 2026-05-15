package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type UpsertCallEventParams struct {
	ChatJID      string
	ChatName     string
	SenderJID    string
	SenderName   string
	CallID       string
	MsgID        string
	EventType    string
	Direction    string
	Media        string
	Outcome      string
	Reason       string
	CallType     string
	DurationSecs int64
	Timestamp    time.Time
	Participants []CallParticipant
}

func (d *DB) UpsertCallEvent(p UpsertCallEventParams) error {
	chatJID := strings.TrimSpace(p.ChatJID)
	eventType := strings.TrimSpace(p.EventType)
	if chatJID == "" {
		return fmt.Errorf("chat JID is required")
	}
	if eventType == "" {
		return fmt.Errorf("call event type is required")
	}
	ts := unix(p.Timestamp)
	if ts <= 0 {
		ts = nowUTC().Unix()
	}
	callID := strings.TrimSpace(p.CallID)
	if callID == "" {
		callID = fmt.Sprintf("%s:%d", eventType, ts)
	}
	if p.DurationSecs < 0 {
		p.DurationSecs = 0
	}

	var participantsJSON interface{}
	if len(p.Participants) > 0 {
		if b, err := json.Marshal(p.Participants); err == nil {
			participantsJSON = string(b)
		}
	}

	_, err := d.sql.Exec(`
		INSERT INTO call_events(
			chat_jid, chat_name, sender_jid, sender_name, call_id, msg_id, event_type,
			direction, media, outcome, reason, call_type, duration_secs, ts, participants
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, call_id, event_type, ts) DO UPDATE SET
			chat_name=COALESCE(NULLIF(excluded.chat_name,''), call_events.chat_name),
			sender_jid=COALESCE(NULLIF(excluded.sender_jid,''), call_events.sender_jid),
			sender_name=COALESCE(NULLIF(excluded.sender_name,''), call_events.sender_name),
			msg_id=COALESCE(NULLIF(excluded.msg_id,''), call_events.msg_id),
			direction=COALESCE(NULLIF(excluded.direction,''), call_events.direction),
			media=COALESCE(NULLIF(excluded.media,''), call_events.media),
			outcome=COALESCE(NULLIF(excluded.outcome,''), call_events.outcome),
			reason=COALESCE(NULLIF(excluded.reason,''), call_events.reason),
			call_type=COALESCE(NULLIF(excluded.call_type,''), call_events.call_type),
			duration_secs=CASE WHEN excluded.duration_secs > 0 THEN excluded.duration_secs ELSE call_events.duration_secs END,
			participants=COALESCE(NULLIF(excluded.participants,''), call_events.participants)
	`, chatJID, nullIfEmpty(p.ChatName), nullIfEmpty(p.SenderJID), nullIfEmpty(p.SenderName),
		callID, nullIfEmpty(p.MsgID), eventType, nullIfEmpty(p.Direction), nullIfEmpty(p.Media),
		nullIfEmpty(p.Outcome), nullIfEmpty(p.Reason), nullIfEmpty(p.CallType), p.DurationSecs, ts, participantsJSON)
	return err
}

type ListCallEventsParams struct {
	ChatJID  string
	ChatJIDs []string
	Limit    int
	Before   *time.Time
	After    *time.Time
	Asc      bool
}

type DeleteCallEventsParams struct {
	ChatJID   string
	Direction string
}

func (d *DB) DeleteCallEvents(p DeleteCallEventsParams) (int64, error) {
	chatJID := strings.TrimSpace(p.ChatJID)
	if chatJID == "" {
		return 0, fmt.Errorf("chat JID is required")
	}
	query := "DELETE FROM call_events WHERE chat_jid = ? AND event_type = 'call_log'"
	args := []interface{}{chatJID}
	if direction := strings.TrimSpace(p.Direction); direction != "" {
		query += " AND direction = ?"
		args = append(args, direction)
	}
	res, err := d.sql.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) ListCallEvents(p ListCallEventsParams) ([]CallEvent, error) {
	if p.Limit <= 0 {
		p.Limit = 50
	}
	query := `
		SELECT rowid, chat_jid, COALESCE(chat_name,''), COALESCE(sender_jid,''), COALESCE(sender_name,''),
		       call_id, COALESCE(msg_id,''), event_type, COALESCE(direction,''), COALESCE(media,''),
		       COALESCE(outcome,''), COALESCE(reason,''), COALESCE(call_type,''), duration_secs,
		       ts, COALESCE(participants,'')
		FROM call_events
		WHERE 1=1`
	var args []interface{}
	query, args = appendStringFilter(query, args, "chat_jid", p.ChatJID, p.ChatJIDs)
	if p.After != nil {
		query += " AND ts > ?"
		args = append(args, unix(*p.After))
	}
	if p.Before != nil {
		query += " AND ts < ?"
		args = append(args, unix(*p.Before))
	}
	if p.Asc {
		query += " ORDER BY ts ASC, rowid ASC LIMIT ?"
	} else {
		query += " ORDER BY ts DESC, rowid DESC LIMIT ?"
	}
	args = append(args, p.Limit)

	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CallEvent
	for rows.Next() {
		var ce CallEvent
		var ts int64
		var participantsJSON string
		if err := rows.Scan(&ce.rowID, &ce.ChatJID, &ce.ChatName, &ce.SenderJID, &ce.SenderName,
			&ce.CallID, &ce.MsgID, &ce.EventType, &ce.Direction, &ce.Media, &ce.Outcome,
			&ce.Reason, &ce.CallType, &ce.DurationSecs, &ts, &participantsJSON); err != nil {
			return nil, err
		}
		ce.Timestamp = fromUnix(ts)
		if participantsJSON != "" {
			_ = json.Unmarshal([]byte(participantsJSON), &ce.Participants)
		}
		out = append(out, ce)
	}
	return out, rows.Err()
}
