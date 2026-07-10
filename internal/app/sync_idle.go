package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/wa"
)

func (a *App) runSyncFollow(ctx context.Context, maxReconnect time.Duration, presenceMode SyncPresenceMode, messagesStored, connectionEpoch *atomic.Int64, disconnected <-chan struct{}, staleReconnect <-chan staleReconnectRequest) (SyncResult, error) {
	for {
		select {
		case <-ctx.Done():
			a.emitOrPrint("stopping", map[string]any{"messages_synced": messagesStored.Load()}, "\nStopping sync.\n")
			return SyncResult{MessagesStored: messagesStored.Load()}, nil
		case req := <-staleReconnect:
			if epoch := connectionEpoch.Load(); epoch > 0 && req.lastSuccess.Before(time.Unix(0, epoch)) {
				continue
			}
			a.emitOrPrint("stale", map[string]any{
				"threshold":     req.threshold.String(),
				"idle_duration": req.idle.String(),
				"error_count":   req.errorCount,
				"source":        req.source,
			}, "\nKeepalive has been failing for %s (threshold %s), reconnecting...\n", req.idle, req.threshold)
			// Force-close before reconnecting, matching StreamReplaced. Without this,
			// Client.Connect can see the existing socket as still live and return nil.
			a.wa.Close()
			connectionEpoch.Store(nowUTC().UnixNano())
			if err := a.reconnect(ctx, maxReconnect, presenceMode); err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		case <-disconnected:
			a.emitOrPrint("reconnecting", nil, "Reconnecting...\n")
			connectionEpoch.Store(nowUTC().UnixNano())
			if err := a.reconnect(ctx, maxReconnect, presenceMode); err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		}
	}
}

func (a *App) runSyncUntilIdle(ctx context.Context, idleExit, maxReconnect time.Duration, presenceMode SyncPresenceMode, messagesStored, lastEvent *atomic.Int64, disconnected <-chan struct{}) (SyncResult, error) {
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
			if err := a.reconnect(ctx, maxReconnect, presenceMode); err != nil {
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
func (a *App) reconnect(ctx context.Context, maxDuration time.Duration, presenceMode SyncPresenceMode) error {
	rctx := ctx
	var cancel context.CancelFunc
	if maxDuration > 0 {
		rctx, cancel = context.WithTimeout(ctx, maxDuration)
		defer cancel()
	}
	err := a.wa.ReconnectWithBackoff(rctx, 2*time.Second, 30*time.Second, wa.ConnectOptions{
		SuppressInitialAvailablePresence: !presenceMode.SendsAvailablePresence(),
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("could not reconnect after %s: %w", maxDuration, err)
	}
	return err
}
