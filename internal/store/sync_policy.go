package store

import (
	"database/sql"
	"fmt"
	"time"
)

// SyncOptimizationPolicy controls the bounded local index used by --optimized.
// The policy is stored with the account rather than in global configuration so
// named accounts can choose independent retention behaviour.
type SyncOptimizationPolicy struct {
	Enabled            bool
	MaxChats           int
	ExcludeArchived    bool
	HistoryDays        int
	MaxMessagesPerChat int
	PruneEvictedChats  bool
	PersistCalls       bool
	PersistStatuses    bool
}

func DefaultSyncOptimizationPolicy() SyncOptimizationPolicy {
	return SyncOptimizationPolicy{
		Enabled: true, MaxChats: 100, ExcludeArchived: true, HistoryDays: 30,
		MaxMessagesPerChat: 50, PruneEvictedChats: true,
	}
}

func (p SyncOptimizationPolicy) Validate() error {
	if !p.Enabled {
		return nil
	}
	if p.MaxChats <= 0 || p.HistoryDays <= 0 || p.MaxMessagesPerChat <= 0 {
		return fmt.Errorf("optimized limits must be positive")
	}
	return nil
}

func (d *DB) SyncOptimizationPolicy() (SyncOptimizationPolicy, error) {
	row := d.sql.QueryRowContext(storeCtx(), `SELECT enabled,max_chats,exclude_archived,history_days,max_messages_per_chat,prune_evicted_chats,persist_calls,persist_statuses FROM sync_optimization_policy WHERE id=1`)
	var p SyncOptimizationPolicy
	var enabled, archived, prune, calls, statuses int
	err := row.Scan(&enabled, &p.MaxChats, &archived, &p.HistoryDays, &p.MaxMessagesPerChat, &prune, &calls, &statuses)
	if err == sql.ErrNoRows {
		return SyncOptimizationPolicy{}, nil
	}
	if err != nil {
		return SyncOptimizationPolicy{}, err
	}
	p.Enabled, p.ExcludeArchived, p.PruneEvictedChats = enabled != 0, archived != 0, prune != 0
	p.PersistCalls, p.PersistStatuses = calls != 0, statuses != 0
	return p, nil
}

func (d *DB) SetSyncOptimizationPolicy(p SyncOptimizationPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO sync_optimization_policy(id,enabled,max_chats,exclude_archived,history_days,max_messages_per_chat,prune_evicted_chats,persist_calls,persist_statuses,updated_at)
		VALUES(1,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,max_chats=excluded.max_chats,exclude_archived=excluded.exclude_archived,history_days=excluded.history_days,max_messages_per_chat=excluded.max_messages_per_chat,prune_evicted_chats=excluded.prune_evicted_chats,persist_calls=excluded.persist_calls,persist_statuses=excluded.persist_statuses,updated_at=excluded.updated_at`,
		boolToInt(p.Enabled), p.MaxChats, boolToInt(p.ExcludeArchived), p.HistoryDays, p.MaxMessagesPerChat, boolToInt(p.PruneEvictedChats), boolToInt(p.PersistCalls), boolToInt(p.PersistStatuses), time.Now().UTC().Unix())
	return err
}
