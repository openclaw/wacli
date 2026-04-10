package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	BackfillStatusReady      = "ready"
	BackfillStatusInProgress = "in_progress"
	BackfillStatusStalled    = "stalled"
	BackfillStatusBlocked    = "blocked"
	BackfillStatusComplete   = "complete"

	BackfillBlockedNoLocalAnchor = "no_local_anchor"
)

type BackfillState struct {
	ChatJID                 string
	Status                  string
	LastBackfillAt          time.Time
	LastSuccessAt           time.Time
	RequestsSentTotal       int
	ResponsesSeenTotal      int
	ConsecutiveNoopRequests int
	ReachedStart            bool
	BlockedReason           string
	LastError               string
	UpdatedAt               time.Time
}

type ChatCoverage struct {
	ChatJID        string
	Kind           string
	Name           string
	LastMessageTS  time.Time
	MessageCount   int64
	OldestTS       time.Time
	NewestTS       time.Time
	HasState       bool
	Status         string
	BlockedReason  string
	ReachedStart   bool
	LastError      string
	LastBackfillAt time.Time
}

type ListChatCoverageParams struct {
	Query          string
	Kind           string
	ChatJIDs       []string
	Limit          int
	IncludeBlocked bool
	OnlyActionable bool
	OnlyTracked    bool
}

func (d *DB) PutBackfillState(state BackfillState) error {
	state.ChatJID = strings.TrimSpace(state.ChatJID)
	state.Status = strings.TrimSpace(state.Status)
	state.BlockedReason = strings.TrimSpace(state.BlockedReason)
	state.LastError = strings.TrimSpace(state.LastError)
	if state.ChatJID == "" {
		return fmt.Errorf("chat JID is required")
	}
	if state.Status == "" {
		return fmt.Errorf("status is required")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}

	_, err := d.sql.Exec(`
		INSERT INTO history_backfill_state(
			chat_jid,
			status,
			last_backfill_at,
			last_success_at,
			requests_sent_total,
			responses_seen_total,
			consecutive_noop_requests,
			reached_start,
			blocked_reason,
			last_error,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid) DO UPDATE SET
			status=excluded.status,
			last_backfill_at=excluded.last_backfill_at,
			last_success_at=excluded.last_success_at,
			requests_sent_total=excluded.requests_sent_total,
			responses_seen_total=excluded.responses_seen_total,
			consecutive_noop_requests=excluded.consecutive_noop_requests,
			reached_start=excluded.reached_start,
			blocked_reason=excluded.blocked_reason,
			last_error=excluded.last_error,
			updated_at=excluded.updated_at
	`,
		state.ChatJID,
		state.Status,
		nullIfZeroUnix(state.LastBackfillAt),
		nullIfZeroUnix(state.LastSuccessAt),
		state.RequestsSentTotal,
		state.ResponsesSeenTotal,
		state.ConsecutiveNoopRequests,
		boolToInt(state.ReachedStart),
		nullIfEmpty(state.BlockedReason),
		nullIfEmpty(state.LastError),
		unix(state.UpdatedAt),
	)
	return err
}

func (d *DB) GetBackfillState(chatJID string) (BackfillState, error) {
	chatJID = strings.TrimSpace(chatJID)
	if chatJID == "" {
		return BackfillState{}, fmt.Errorf("chat JID is required")
	}
	row := d.sql.QueryRow(`
		SELECT chat_jid,
		       status,
		       COALESCE(last_backfill_at, 0),
		       COALESCE(last_success_at, 0),
		       requests_sent_total,
		       responses_seen_total,
		       consecutive_noop_requests,
		       reached_start,
		       COALESCE(blocked_reason, ''),
		       COALESCE(last_error, ''),
		       updated_at
		FROM history_backfill_state
		WHERE chat_jid = ?
	`, chatJID)
	return scanBackfillState(row)
}

func (d *DB) ResetBackfillInProgress(now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := d.sql.Exec(`
		UPDATE history_backfill_state
		SET status = ?,
		    updated_at = ?
		WHERE status = ?
	`, BackfillStatusReady, unix(now), BackfillStatusInProgress)
	return err
}

func (d *DB) ListChatCoverage(p ListChatCoverageParams) ([]ChatCoverage, error) {
	if p.Limit <= 0 {
		p.Limit = 100
	}

	query := `
		SELECT c.jid,
		       c.kind,
		       COALESCE(c.name,''),
		       COALESCE(c.last_message_ts, 0),
		       COALESCE(ms.message_count, 0),
		       COALESCE(ms.oldest_ts, 0),
		       COALESCE(ms.newest_ts, 0),
		       CASE WHEN h.chat_jid IS NULL THEN 0 ELSE 1 END,
		       COALESCE(h.status, ''),
		       COALESCE(h.blocked_reason, ''),
		       COALESCE(h.reached_start, 0),
		       COALESCE(h.last_error, ''),
		       COALESCE(h.last_backfill_at, 0)
		FROM chats c
		LEFT JOIN (
			SELECT chat_jid,
			       COUNT(1) AS message_count,
			       MIN(ts) AS oldest_ts,
			       MAX(ts) AS newest_ts
			FROM messages
			GROUP BY chat_jid
		) ms ON ms.chat_jid = c.jid
		LEFT JOIN history_backfill_state h ON h.chat_jid = c.jid
		WHERE 1=1`
	args := make([]interface{}, 0, 8)

	if q := strings.TrimSpace(p.Query); q != "" {
		needle := "%" + q + "%"
		query += ` AND (LOWER(COALESCE(c.name,'')) LIKE LOWER(?) OR LOWER(c.jid) LIKE LOWER(?))`
		args = append(args, needle, needle)
	}
	if kind := strings.TrimSpace(p.Kind); kind != "" {
		query += ` AND c.kind = ?`
		args = append(args, kind)
	}
	if len(p.ChatJIDs) > 0 {
		query += ` AND c.jid IN (` + placeholders(len(p.ChatJIDs)) + `)`
		for _, jid := range p.ChatJIDs {
			args = append(args, jid)
		}
	}

	query += ` ORDER BY COALESCE(c.last_message_ts, 0) DESC, c.jid LIMIT ?`
	args = append(args, p.Limit)

	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ChatCoverage, 0, p.Limit)
	for rows.Next() {
		var c ChatCoverage
		var lastTS, oldestTS, newestTS, lastBackfill int64
		var reachedStart, hasState int
		if err := rows.Scan(
			&c.ChatJID,
			&c.Kind,
			&c.Name,
			&lastTS,
			&c.MessageCount,
			&oldestTS,
			&newestTS,
			&hasState,
			&c.Status,
			&c.BlockedReason,
			&reachedStart,
			&c.LastError,
			&lastBackfill,
		); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(lastTS)
		c.OldestTS = fromUnix(oldestTS)
		c.NewestTS = fromUnix(newestTS)
		c.LastBackfillAt = fromUnix(lastBackfill)
		c.HasState = hasState != 0
		c.ReachedStart = reachedStart != 0
		c = normalizeCoverage(c)
		if p.OnlyTracked && !c.HasState {
			continue
		}
		if !p.IncludeBlocked && c.Status == BackfillStatusBlocked {
			continue
		}
		if p.OnlyActionable && !isActionableCoverage(c) {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) GetChatCoverage(chatJID string) (ChatCoverage, error) {
	rows, err := d.ListChatCoverage(ListChatCoverageParams{
		ChatJIDs:       []string{strings.TrimSpace(chatJID)},
		Limit:          1,
		IncludeBlocked: true,
	})
	if err != nil {
		return ChatCoverage{}, err
	}
	if len(rows) == 0 {
		return ChatCoverage{}, sql.ErrNoRows
	}
	return rows[0], nil
}

func scanBackfillState(row *sql.Row) (BackfillState, error) {
	var state BackfillState
	var lastBackfillAt, lastSuccessAt, updatedAt int64
	var reachedStart int
	if err := row.Scan(
		&state.ChatJID,
		&state.Status,
		&lastBackfillAt,
		&lastSuccessAt,
		&state.RequestsSentTotal,
		&state.ResponsesSeenTotal,
		&state.ConsecutiveNoopRequests,
		&reachedStart,
		&state.BlockedReason,
		&state.LastError,
		&updatedAt,
	); err != nil {
		return BackfillState{}, err
	}
	state.LastBackfillAt = fromUnix(lastBackfillAt)
	state.LastSuccessAt = fromUnix(lastSuccessAt)
	state.UpdatedAt = fromUnix(updatedAt)
	state.ReachedStart = reachedStart != 0
	return state, nil
}

func normalizeCoverage(c ChatCoverage) ChatCoverage {
	c.Status = strings.TrimSpace(c.Status)
	c.BlockedReason = strings.TrimSpace(c.BlockedReason)
	c.LastError = strings.TrimSpace(c.LastError)

	if c.ReachedStart {
		c.Status = BackfillStatusComplete
		c.BlockedReason = ""
		return c
	}
	if c.MessageCount <= 0 {
		c.Status = BackfillStatusBlocked
		if c.BlockedReason == "" {
			c.BlockedReason = BackfillBlockedNoLocalAnchor
		}
		return c
	}
	if c.Status == BackfillStatusBlocked && c.BlockedReason == BackfillBlockedNoLocalAnchor {
		c.Status = BackfillStatusReady
		c.BlockedReason = ""
	}
	if c.Status == "" {
		c.Status = BackfillStatusReady
	}
	return c
}

func isActionableCoverage(c ChatCoverage) bool {
	switch c.Status {
	case BackfillStatusReady, BackfillStatusInProgress, BackfillStatusStalled:
		return true
	default:
		return false
	}
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func nullIfZeroUnix(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return unix(t)
}
