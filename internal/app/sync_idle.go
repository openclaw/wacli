package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

func (a *App) runSyncFollow(ctx context.Context, maxReconnect time.Duration, messagesStored *atomic.Int64, disconnected <-chan struct{}) (SyncResult, error) {
	for {
		select {
		case <-ctx.Done():
			a.emitOrPrint("stopping", map[string]any{"messages_synced": messagesStored.Load()}, "\nStopping sync.\n")
			return SyncResult{MessagesStored: messagesStored.Load()}, nil
		case <-disconnected:
			a.emitOrPrint("reconnecting", nil, "Reconnecting...\n")
			if err := a.reconnect(ctx, maxReconnect); err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		}
	}
}

func (a *App) runSyncUntilIdle(ctx context.Context, idleExit, maxReconnect time.Duration, messagesStored, lastEvent *atomic.Int64, disconnected <-chan struct{}) (SyncResult, error) {
	poll := 250 * time.Millisecond
	if idleExit >= 2*time.Second {
		poll = 1 * time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.emitOrPrint("stopping", map[string]any{"messages_synced": messagesStored.Load()}, "\nStopping sync.\n")
			return SyncResult{MessagesStored: messagesStored.Load()}, nil
		case <-disconnected:
			a.emitOrPrint("reconnecting", nil, "Reconnecting...\n")
			if err := a.reconnect(ctx, maxReconnect); err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		case <-ticker.C:
			last := time.Unix(0, lastEvent.Load())
			if time.Since(last) >= idleExit {
				a.emitOrPrint("idle_exit", map[string]any{
					"idle_duration":   idleExit.String(),
					"messages_synced": messagesStored.Load(),
				}, "\nIdle for %s, exiting.\n", idleExit)
				return SyncResult{MessagesStored: messagesStored.Load()}, nil
			}
		}
	}
}

// reconnect wraps ReconnectWithBackoff with an optional deadline. If maxDuration
// is positive, reconnection gives up after that long; otherwise it retries until
// ctx is cancelled.
func (a *App) reconnect(ctx context.Context, maxDuration time.Duration) error {
	rctx := ctx
	var cancel context.CancelFunc
	if maxDuration > 0 {
		rctx, cancel = context.WithTimeout(ctx, maxDuration)
		defer cancel()
	}
	err := a.wa.ReconnectWithBackoff(rctx, 2*time.Second, 30*time.Second)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("could not reconnect after %s: %w", maxDuration, err)
	}
	return err
}
