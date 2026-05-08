package store

import (
	"strings"
	"time"
)

const (
	HistoryCoverageStatusReady     = "ready"
	HistoryCoverageStatusBlocked   = "blocked"
	HistoryCoverageBlockedNoAnchor = "no_local_anchor"
)

type HistoryCoverage struct {
	ChatJID       string    `json:"chat_jid"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name,omitempty"`
	LastMessageTS time.Time `json:"last_message_ts,omitempty"`
	MessageCount  int64     `json:"message_count"`
	OldestTS      time.Time `json:"oldest_ts,omitempty"`
	NewestTS      time.Time `json:"newest_ts,omitempty"`
	Status        string    `json:"status"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
}

type ListHistoryCoverageParams struct {
	Query          string
	Kind           string
	ChatJIDs       []string
	Limit          int
	IncludeBlocked bool
	OnlyActionable bool
}

func (d *DB) ListHistoryCoverage(p ListHistoryCoverageParams) ([]HistoryCoverage, error) {
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
		       COALESCE(ms.newest_ts, 0)
		FROM chats c
		LEFT JOIN (
			SELECT chat_jid,
			       COUNT(1) AS message_count,
			       MIN(ts) AS oldest_ts,
			       MAX(ts) AS newest_ts
			FROM messages
			GROUP BY chat_jid
		) ms ON ms.chat_jid = c.jid
		WHERE 1=1`
	args := make([]interface{}, 0, 8)

	if q := strings.TrimSpace(p.Query); q != "" {
		needle := likeContains(q)
		query += ` AND (LOWER(COALESCE(c.name,'')) LIKE LOWER(?) ESCAPE '\' OR LOWER(c.jid) LIKE LOWER(?) ESCAPE '\')`
		args = append(args, needle, needle)
	}
	if kind := strings.TrimSpace(p.Kind); kind != "" {
		query += ` AND c.kind = ?`
		args = append(args, kind)
	}
	if len(p.ChatJIDs) > 0 {
		query, args = appendStringFilter(query, args, "c.jid", "", p.ChatJIDs)
	}
	if !p.IncludeBlocked || p.OnlyActionable {
		query += ` AND COALESCE(ms.message_count, 0) > 0`
	}

	query += ` ORDER BY COALESCE(c.last_message_ts, 0) DESC, c.jid LIMIT ?`
	args = append(args, p.Limit)

	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]HistoryCoverage, 0, p.Limit)
	for rows.Next() {
		var c HistoryCoverage
		var lastTS, oldestTS, newestTS int64
		if err := rows.Scan(&c.ChatJID, &c.Kind, &c.Name, &lastTS, &c.MessageCount, &oldestTS, &newestTS); err != nil {
			return nil, err
		}
		c.LastMessageTS = fromUnix(lastTS)
		c.OldestTS = fromUnix(oldestTS)
		c.NewestTS = fromUnix(newestTS)
		c = normalizeHistoryCoverage(c)
		if p.OnlyActionable && c.Status != HistoryCoverageStatusReady {
			continue
		}
		if !p.IncludeBlocked && c.Status == HistoryCoverageStatusBlocked {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeHistoryCoverage(c HistoryCoverage) HistoryCoverage {
	if c.MessageCount <= 0 {
		c.Status = HistoryCoverageStatusBlocked
		c.BlockedReason = HistoryCoverageBlockedNoAnchor
		return c
	}
	c.Status = HistoryCoverageStatusReady
	c.BlockedReason = ""
	return c
}
