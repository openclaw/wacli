package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	// oneShotStoreDrainMax bounds how long Close waits for events still being
	// stored when the connection closed, within the command's own deadline.
	oneShotStoreDrainMax = 10 * time.Second
	// oneShotStoreDrainNoDeadline is the bound for a command run without a
	// deadline (--timeout 0).
	oneShotStoreDrainNoDeadline = 2 * time.Second
)

// A command that connects by itself (no `sync` holds the store) receives what
// WhatsApp queued for this device while it was offline, like any connection.
// oneShotStore keeps sync's storage handler registered for the life of such a
// connection:
//
//   - Messages and history chunks: whatsmeow sends the receipt only after the
//     handlers return, so what the command acknowledges is stored first.
//   - Receipts, calls, app-state changes and channel messages: whatsmeow
//     acknowledges them on arrival, before any handler runs; they are stored
//     as they are handled.
//
// What is not covered: when the connection closes, whatsmeow keeps decrypting
// the message it was handling and hands it over after the store has closed.
// That one message (1:1 or group) is lost without a warning: its decryption
// was committed, so the copy WhatsApp delivers again no longer decrypts.
type oneShotStore struct {
	handlerID uint32
	gate      *mediaQueue
	limits    *syncStorageLimits
	cancel    context.CancelFunc
	deadline  time.Time // the command's own deadline; zero when it has none
	drained   bool
	waitedFor time.Duration
	// appStateQueued: app-state writes were still queued when Close gave up.
	appStateQueued bool
}

// startOneShotStore registers the store handler before the first connect of a
// command that connects by itself. Later reconnects keep the same handler. A
// running Sync stores events itself, so commands it executes for others (the
// delegate socket) do not register a second handler.
func (a *App) startOneShotStore(ctx context.Context) {
	if a.syncRunning.Load() > 0 {
		return
	}
	a.waMu.Lock()
	defer a.waMu.Unlock()
	if a.oneShot != nil || a.wa == nil {
		return
	}
	storeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &oneShotStore{
		gate:   newMediaQueue(1),
		limits: &syncStorageLimits{app: a},
		cancel: cancel,
	}
	if deadline, ok := ctx.Deadline(); ok {
		s.deadline = deadline
	}
	// Download history chunks inside the handler, before whatsmeow sends the
	// message receipt, as sync does; otherwise whatsmeow downloads them in the
	// background and deletes the server copy whether or not they were stored.
	a.wa.SetManualHistorySyncDownload(true)
	var stored, lastEvent atomic.Int64
	s.handlerID, _ = a.addSyncEventHandler(storeCtx, SyncOptions{PresenceMode: SyncPresenceModeQuiet, oneShot: true},
		&stored, &lastEvent, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1),
		func(string, string) {}, nil, s.limits, &syncPresence{}, s.gate)
	a.oneShot = s
}

// oneShotStores reports whether a one-shot connection hands evt to sync's
// handler: everything sync stores, nothing that runs sync's own connection
// loop, presence, freshness record or progress output.
func (a *App) oneShotStores(evt any) bool {
	switch evt.(type) {
	case *events.Connected, *events.Disconnected, *events.StreamReplaced, *events.LoggedOut,
		*events.KeepAliveTimeout, *events.PushNameSetting,
		*events.OfflineSyncPreview, *events.OfflineSyncCompleted,
		*events.AppStateSyncError, *events.ChatPresence, *events.Presence:
		return false
	case *events.AppState, *events.Star, *events.DeleteForMe,
		*events.Archive, *events.UnarchiveChatsSetting, *events.Pin, *events.Mute, *events.MarkChatAsRead,
		*events.Contact, *wa.AppStateKeyUnavailable:
		// Chat-state commands and contacts save persist app state themselves.
		return a.chatStatePersisters.Load() == 0
	default:
		return true
	}
}

// fenceOneShotStore runs once the socket is closed, so no receipt can follow.
// It waits for the events still being stored, up to oneShotStoreDrainMax and
// never past the command's own deadline (oneShotStoreDrainNoDeadline without
// one), then closes the store. Removing a handler waits for the event being
// dispatched (whatsmeow holds its handler lock while handlers run), so when
// the wait was cut short the handler is left registered: the process is about
// to exit, and Close does not wait for it. Closing the databases does not wait
// for a statement still in flight either (database/sql closes a connection in
// use only when it is returned).
func (a *App) fenceOneShotStore() *oneShotStore {
	a.waMu.Lock()
	s, waClient := a.oneShot, a.wa
	a.oneShot = nil
	a.waMu.Unlock()
	if s == nil {
		return nil
	}
	wait := oneShotStoreDrainNoDeadline
	if !s.deadline.IsZero() {
		wait = min(oneShotStoreDrainMax, max(time.Until(s.deadline), 0))
	}
	s.waitedFor = wait
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	s.drained = s.gate.waitIdle(ctx)
	cancel()
	if !s.drained {
		s.gate.stopAccepting()
		// Stop a slow download; what it did not store is lost.
		s.cancel()
		return s
	}
	waClient.RemoveEventHandler(s.handlerID)
	return s
}

// stopAccepting closes the gate without waiting for producers to finish.
func (q *mediaQueue) stopAccepting() {
	q.mu.Lock()
	q.accepting = false
	q.mu.Unlock()
	q.notify()
}

// handlersIdle reports whether no handler is still running an event, so that
// removing a handler cannot block.
func (s *oneShotStore) handlersIdle() bool { return s == nil || s.drained }

// abandonQueuedAppState does not wait for app-state writes still queued when
// the wait at exit was cut short: the handler that was cut short may hold a
// place in that queue. What is still queued is reported.
func (s *oneShotStore) abandonQueuedAppState(a *App) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.appStateQueued = a.appStatePersist.waitIdle(ctx) != nil
}

// finish reports what the connection could not store. It runs after pending
// app-state writes, so their failures are included.
func (s *oneShotStore) finish(a *App) {
	if s == nil {
		return
	}
	s.cancel()
	if !s.drained {
		queued := ""
		if s.appStateQueued {
			queued = ", and app-state writes were still queued"
		}
		a.emitWarning("oneshot_store_incomplete",
			fmt.Sprintf("warning: the connection closed while an incoming event was still being stored (waited %s)%s; what was not stored is not in wacli.db and WhatsApp will not deliver it again", s.waitedFor.Round(time.Millisecond), queued),
			map[string]any{"waited_ms": s.waitedFor.Milliseconds(), "app_state_queued": s.appStateQueued})
	}
	if s.limits.persistenceFailed.Load() {
		a.emitWarning("oneshot_store_failed",
			"warning: WhatsApp delivered messages or events to this command that could not be stored in wacli.db; they were acknowledged and will not be delivered again. Check the store (disk space, permissions) before the next command or sync.",
			nil)
	}
}
