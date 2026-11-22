package app

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types/events"
)

// oneShotStoreDrainTimeout bounds how long Close waits for an event that was
// already being stored when the connection closed.
const oneShotStoreDrainTimeout = 10 * time.Second

// A command that connects by itself (no `sync` holds the store) receives what
// WhatsApp queued for this device while it was offline, like any connection,
// and whatsmeow acknowledges each message once the event handlers return.
// oneShotStore keeps sync's storage handler registered for the life of such a
// connection, so whatever the command acknowledges is in wacli.db when it
// exits; anything it never received stays queued for the next connection.
type oneShotStore struct {
	handlerID uint32
	gate      *mediaQueue
	limits    *syncStorageLimits
	cancel    context.CancelFunc
	drained   bool
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

// fenceOneShotStore runs once the socket is closed, so no receipt can follow:
// it waits for an event still being stored, refuses later ones and removes
// the handler.
func (a *App) fenceOneShotStore() *oneShotStore {
	a.waMu.Lock()
	s, waClient := a.oneShot, a.wa
	a.oneShot = nil
	a.waMu.Unlock()
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), oneShotStoreDrainTimeout)
	s.drained = s.gate.waitIdle(ctx)
	cancel()
	if !s.drained {
		// Removing the handler waits for its dispatch: stop a slow download.
		s.cancel()
	}
	waClient.RemoveEventHandler(s.handlerID)
	return s
}

// finish reports what the connection could not store. It runs after pending
// app-state writes, so their failures are included.
func (s *oneShotStore) finish(a *App) {
	if s == nil {
		return
	}
	s.cancel()
	if !s.drained {
		a.emitWarning("oneshot_store_incomplete",
			fmt.Sprintf("warning: the connection closed while an incoming event was still being stored (waited %s); it may be missing from wacli.db", oneShotStoreDrainTimeout),
			nil)
	}
	if s.limits.persistenceFailed.Load() {
		a.emitWarning("oneshot_store_failed",
			"warning: WhatsApp delivered messages or events to this command that could not be stored in wacli.db; they were acknowledged and will not be delivered again. Check the store (disk space, permissions) before the next command or sync.",
			nil)
	}
}
