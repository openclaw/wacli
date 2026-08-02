package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/wa"
)

// loggedOutPending does a non-blocking receive on the terminal logout signal.
// A revocation delivers Disconnected and LoggedOut back to back, and with both
// buffered channels ready select picks a branch at random — so every reconnect
// path must check this first. The terminal signal wins; never dial a dead
// session.
func loggedOutPending(loggedOut <-chan struct{}) bool {
	select {
	case <-loggedOut:
		return true
	default:
		return false
	}
}

// stopLoggedOut emits the terminal stop for a revoked session and returns the
// final result.
func (a *App) stopLoggedOut(messagesStored *atomic.Int64) (SyncResult, error) {
	a.emitOrPrint("stopping", map[string]any{
		"messages_synced": messagesStored.Load(),
		"reason":          "logged_out",
	}, "\nStopping sync (logged out).\n")
	return SyncResult{MessagesStored: messagesStored.Load()}, nil
}

func (a *App) runSyncFollow(ctx context.Context, maxReconnect time.Duration, presenceMode SyncPresenceMode, messagesStored, connectionEpoch *atomic.Int64, disconnected <-chan struct{}, loggedOut <-chan struct{}, staleReconnect <-chan staleReconnectRequest) (SyncResult, error) {
	for {
		select {
		case <-ctx.Done():
			a.emitOrPrint("stopping", map[string]any{"messages_synced": messagesStored.Load()}, "\nStopping sync.\n")
			return SyncResult{MessagesStored: messagesStored.Load()}, nil
		case req := <-staleReconnect:
			if loggedOutPending(loggedOut) {
				return a.stopLoggedOut(messagesStored)
			}
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
			loggedOutDuringReconnect, err := a.reconnectWhileWatchingLogout(ctx, maxReconnect, presenceMode, loggedOut)
			if loggedOutDuringReconnect {
				return a.stopLoggedOut(messagesStored)
			}
			if err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		case <-disconnected:
			if loggedOutPending(loggedOut) {
				return a.stopLoggedOut(messagesStored)
			}
			a.emitOrPrint("reconnecting", nil, "Reconnecting...\n")
			connectionEpoch.Store(nowUTC().UnixNano())
			loggedOutDuringReconnect, err := a.reconnectWhileWatchingLogout(ctx, maxReconnect, presenceMode, loggedOut)
			if loggedOutDuringReconnect {
				return a.stopLoggedOut(messagesStored)
			}
			if err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		case <-loggedOut:
			// Session revoked (see the LoggedOut handler). Stop cleanly instead
			// of reconnecting forever against a dead session.
			return a.stopLoggedOut(messagesStored)
		}
	}
}

func (a *App) runSyncUntilIdle(ctx context.Context, idleExit, maxReconnect time.Duration, presenceMode SyncPresenceMode, messagesStored, lastEvent *atomic.Int64, disconnected <-chan struct{}, loggedOut <-chan struct{}) (SyncResult, error) {
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
			if loggedOutPending(loggedOut) {
				return a.stopLoggedOut(messagesStored)
			}
			a.emitOrPrint("reconnecting", nil, "Reconnecting...\n")
			loggedOutDuringReconnect, err := a.reconnectWhileWatchingLogout(ctx, maxReconnect, presenceMode, loggedOut)
			if loggedOutDuringReconnect {
				return a.stopLoggedOut(messagesStored)
			}
			if err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		case <-loggedOut:
			// Session revoked (see the LoggedOut handler). Stop cleanly instead
			// of reconnecting forever against a dead session.
			return a.stopLoggedOut(messagesStored)
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

// reconnectWhileWatchingLogout lets the terminal logout signal interrupt an
// active reconnect. Checking the channel only before reconnecting is not enough:
// LoggedOut may arrive while ReconnectWithBackoff is sleeping or dialing.
func (a *App) reconnectWhileWatchingLogout(ctx context.Context, maxDuration time.Duration, presenceMode SyncPresenceMode, loggedOut <-chan struct{}) (bool, error) {
	reconnectCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- a.reconnect(reconnectCtx, maxDuration, presenceMode)
	}()

	select {
	case <-loggedOut:
		cancel()
		<-done
		return true, nil
	case err := <-done:
		// If reconnect and logout complete together, the terminal signal still
		// wins. Otherwise it remains queued for the loop's next select.
		if loggedOutPending(loggedOut) {
			return true, nil
		}
		return false, err
	}
}
