package main

import (
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
)

type optimizedFlags struct {
	enabled, excludeArchived, pruneEvicted, persistCalls, persistStatuses bool
	maxChats, historyDays, messagesPerChat                                int
	changed, optimizedSet, tuning                                         bool
}

func addOptimizedFlags(cmd *cobra.Command, f *optimizedFlags) {
	cmd.Flags().BoolVar(&f.enabled, "optimized", false, "persist a bounded, privacy-focused local message index")
	cmd.Flags().IntVar(&f.maxChats, "max-chats", 0, "maximum active chats retained by optimized sync")
	cmd.Flags().BoolVar(&f.excludeArchived, "exclude-archived", true, "exclude archived chats in optimized sync")
	cmd.Flags().IntVar(&f.historyDays, "history-days", 0, "initial history days requested while pairing in optimized sync")
	cmd.Flags().IntVar(&f.messagesPerChat, "history-messages-per-chat", 0, "messages retained per chat in optimized sync")
	cmd.Flags().BoolVar(&f.pruneEvicted, "prune-evicted-chats", true, "prune messages from chats evicted by optimized sync")
	cmd.Flags().BoolVar(&f.persistCalls, "persist-calls", false, "persist call records in optimized sync")
	cmd.Flags().BoolVar(&f.persistStatuses, "persist-statuses", false, "persist WhatsApp Status records in optimized sync")
	cmd.Flags().Bool("confirm", false, "confirm irreversible optimized-store cleanup")
}

func (f *optimizedFlags) capture(cmd *cobra.Command) {
	f.optimizedSet = cmd.Flags().Changed("optimized")
	f.tuning = cmd.Flags().Changed("max-chats") || cmd.Flags().Changed("exclude-archived") || cmd.Flags().Changed("history-days") || cmd.Flags().Changed("history-messages-per-chat") || cmd.Flags().Changed("prune-evicted-chats") || cmd.Flags().Changed("persist-calls") || cmd.Flags().Changed("persist-statuses")
	f.changed = f.optimizedSet || f.tuning
}

func resolveOptimizedPolicy(existing store.SyncOptimizationPolicy, f optimizedFlags) (store.SyncOptimizationPolicy, bool, error) {
	if !f.changed {
		return existing, false, nil
	}
	p := existing
	if f.optimizedSet && !f.enabled {
		if f.tuning {
			return p, false, fmt.Errorf("optimized tuning flags require --optimized")
		}
		p.Enabled = false
		return p, true, nil
	}
	if !p.Enabled || f.enabled {
		p = store.DefaultSyncOptimizationPolicy()
	}
	p.Enabled = true
	if f.maxChats > 0 {
		p.MaxChats = f.maxChats
	}
	if f.historyDays > 0 {
		p.HistoryDays = f.historyDays
	}
	if f.messagesPerChat > 0 {
		p.MaxMessagesPerChat = f.messagesPerChat
	}
	p.ExcludeArchived, p.PruneEvictedChats, p.PersistCalls, p.PersistStatuses = f.excludeArchived, f.pruneEvicted, f.persistCalls, f.persistStatuses
	if err := p.Validate(); err != nil {
		return p, false, err
	}
	return p, true, nil
}

func optimizedPolicyForApp(a interface{ DB() *store.DB }, cmd *cobra.Command, f optimizedFlags) (*store.SyncOptimizationPolicy, error) {
	existing, err := a.DB().SyncOptimizationPolicy()
	if err != nil {
		return nil, err
	}
	p, changed, err := resolveOptimizedPolicy(existing, f)
	if err != nil || !changed {
		return nil, err
	}
	if p.Enabled && !existing.Enabled {
		var messageCount int64
		if err := a.DB().Raw().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
			return nil, err
		}
		confirmed, _ := cmd.Flags().GetBool("confirm")
		if messageCount > 0 && !confirmed {
			return nil, fmt.Errorf("optimized activation will prune local history; rerun with --confirm")
		}
		if err := removeOptimizedLocalMedia(a.DB()); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

func removeOptimizedLocalMedia(db *store.DB) error {
	paths, err := db.AllLocalMediaPaths()
	if err != nil {
		return fmt.Errorf("list local media for optimized cleanup: %w", err)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove local media %q: %w", path, err)
		}
	}
	if err := db.ClearAllLocalMediaPaths(); err != nil {
		return fmt.Errorf("clear local media records: %w", err)
	}
	return nil
}
