package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	appStateRetryInitialDelay = 250 * time.Millisecond
	appStateRetryMaxDelay     = 5 * time.Second
	appStateRecoveryMaxWait   = 5 * time.Minute
	// How long a background continuation keeps waiting for a recovery the
	// primary answers after the synchronous waiter gave up. Real phones have
	// been observed to answer 71 seconds and even hours later.
	lateAppStateRecoveryMaxWait = 24 * time.Hour
	// Minimum spacing between background recovery requests sent by the
	// degraded (full-sync-known-broken) pre-write path, per collection.
	backgroundAppStateRecoveryInterval = 6 * time.Hour
)

// AddChatStatePersistenceHandler captures app-state events that arrive while a
// one-shot chat-state command is connected. Remove it only after closing the
// connection so whatsmeow cannot advance another collection without persisting it.
func (a *App) AddChatStatePersistenceHandler(ctx context.Context) (func(), error) {
	if err := a.OpenWA(); err != nil {
		return nil, err
	}
	waClient := a.WA()
	handlerID := waClient.AddEventHandler(func(evt any) {
		switch v := evt.(type) {
		case *events.AppState, *events.Star, *events.DeleteForMe,
			*events.Archive, *events.UnarchiveChatsSetting, *events.Pin, *events.Mute, *events.MarkChatAsRead,
			*events.Contact:
			a.handleAppStatePersistenceEvent(ctx, evt, nil)
		case *wa.AppStateKeyUnavailable:
			a.warnEmptyAppStateKey(v)
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			waClient.RemoveEventHandler(handlerID)
		})
	}, nil
}

func (a *App) ArchiveChat(ctx context.Context, jid types.JID, archive bool) error {
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	archiveTime := nowUTC()
	postSendEvents, err := a.wa.ArchiveChat(ctx, jid, archive, archiveTime, nil, func() { pending.reserve(a) })
	if err != nil {
		return errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	return a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		return a.db.ApplyChatArchive(chatJID, archive, archiveTime)
	})
}

func (a *App) PinChat(ctx context.Context, jid types.JID, pin bool) error {
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	postSendEvents, err := a.wa.PinChat(ctx, jid, pin, func() { pending.reserve(a) })
	if err != nil {
		return errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	return a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		return a.db.SetChatPinned(chatJID, pin)
	})
}

func (a *App) MuteChat(ctx context.Context, jid types.JID, mute bool, duration time.Duration) error {
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularHigh)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	mutedUntil := mutedUntilUnix(mute, duration, nowUTC())
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularHigh)
	if err != nil {
		return err
	}
	postSendEvents, err := a.wa.MuteChat(ctx, jid, mute, duration, func() { pending.reserve(a) })
	if err != nil {
		return errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	return a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		return a.db.SetChatMutedUntil(chatJID, mutedUntil)
	})
}

func (a *App) MarkChatRead(ctx context.Context, jid types.JID, read bool) error {
	return a.markChatRead(ctx, jid, read, "")
}

// MarkChatReadThrough fixes the inbound read position before recovery or a
// network write. Recovery must never extend what the caller has actually read.
func (a *App) MarkChatReadThrough(ctx context.Context, jid types.JID, messageID string) error {
	if strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	return a.markChatRead(ctx, jid, true, messageID)
}

func (a *App) markChatRead(ctx context.Context, jid types.JID, read bool, throughID string) error {
	var chatJID string
	var lastTS time.Time
	var lastKey *waCommon.MessageKey
	if throughID != "" {
		chatJID = canonicalJIDString(a.canonicalStoreJID(ctx, jid))
		m, err := a.db.ReadBoundary(chatJID, throughID)
		if err != nil {
			return err
		}
		lastTS, lastKey = m.Timestamp, messageKeyFromStore(m)
	}
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	defer release()
	if throughID == "" {
		// Keep legacy resolution and latest-message capture after recovery.
		chatJID = canonicalJIDString(a.canonicalStoreJID(ctx, jid))
		lastTS, lastKey = a.latestMessageRange(chatJID)
	}
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	postSendEvents, err := a.wa.MarkChatAsRead(ctx, jid, read, lastTS, lastKey, func() { pending.reserve(a) })
	if err != nil {
		return errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	return a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		if !read {
			return a.db.SetChatUnread(chatJID, true)
		}
		var ids []string
		if lastKey != nil {
			ids = []string{lastKey.GetID()}
		}
		if throughID != "" {
			return a.db.ClearChatUnreadThroughPreservingMarker(chatJID, lastTS, ids)
		}
		return a.db.ClearChatUnreadThrough(chatJID, lastTS, ids)
	})
}

func (a *App) acquireChatStateSync(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for chat state synchronization: %w", ctx.Err())
	case <-a.chatStateSync:
	}
	release := func() { a.chatStateSync <- struct{}{} }
	if err := ctx.Err(); err != nil {
		release()
		return nil, fmt.Errorf("wait for chat state synchronization: %w", err)
	}
	return release, nil
}

func (a *App) beginChatStateWrite(ctx context.Context, collection appstate.WAPatchName) (func(), error) {
	release, err := a.acquireChatStateSync(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.syncChatStateBeforeWrite(ctx, collection); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (a *App) syncChatStateBeforeWrite(ctx context.Context, collection appstate.WAPatchName) error {
	markerGeneration, recoveryRequired, err := a.db.BeginAppStateRecovery(string(collection))
	if err != nil {
		return fmt.Errorf("begin WhatsApp app state recovery for %s: %w", collection, err)
	}
	tracker := &appStatePersistenceTracker{}
	if recoveryRequired {
		if handled, degradedErr := a.degradedAppStateReplay(ctx, collection, markerGeneration, tracker); handled {
			return degradedErr
		}
		return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
	}

	// The fetch, its retries while whatsmeow waits for a missing key, and
	// the waits between them share one bound; a replay below has its own.
	keyCtx, cancelKeyWait := context.WithTimeout(ctx, appStateRecoveryMaxWait)
	defer cancelKeyWait()
	retryDelay := appStateRetryInitialDelay
	for {
		err, persistenceErr := a.fetchAndPersistAppState(keyCtx, collection, false, tracker)
		if persistenceErr != nil {
			return fmt.Errorf("persist fetched app state %s: %w", collection, persistenceErr)
		}
		if err == nil {
			return a.clearCompletedAppStateRecovery(collection, markerGeneration)
		}

		if errors.Is(err, appstate.ErrMismatchingLTHash) || errors.Is(err, wa.ErrEmptyAppStateKeyShare) {
			return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
		} else if errors.Is(err, appstate.ErrKeyNotFound) {
			if !a.keepsLocalAppStateOnRecovery(ctx, collection) {
				return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
			}
			// An ordinary missing key is not a reason to give up local state
			// or to wait for a snapshot: retry the incremental fetch until
			// the key whatsmeow requested arrives.
			if err := waitForAppStateKeyDelivery(keyCtx, collection, &retryDelay); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("sync WhatsApp chat state before update: %w", err)
		}
	}
}

// replayRequiredAppState settles a hash mismatch, a known-empty key, or
// replay debt the degraded path could not handle: with a full replay, retried
// while whatsmeow waits for a missing key, or from the primary's snapshot on
// a collection whose local state must not be destroyed. An ordinary missing
// key on such a collection is retried incrementally by the callers instead,
// so it never reaches the snapshot wait.
func (a *App) replayRequiredAppState(ctx context.Context, collection appstate.WAPatchName, markerGeneration int64, tracker *appStatePersistenceTracker) error {
	ctx, cancel := context.WithTimeout(ctx, appStateRecoveryMaxWait)
	defer cancel()
	if a.keepsLocalAppStateOnRecovery(ctx, collection) {
		return a.recoverMismatchingAppState(ctx, collection, markerGeneration, tracker, nil)
	}
	retryDelay := appStateRetryInitialDelay
	for {
		fetchErr, persistenceErr := a.fetchAndPersistAppState(ctx, collection, true, tracker)
		if persistenceErr != nil {
			return fmt.Errorf("persist replayed app state %s: %w", collection, persistenceErr)
		}
		if fetchErr == nil {
			return a.clearCompletedAppStateRecovery(collection, markerGeneration)
		}
		// An ErrMismatchingLTHash here just armed (and persisted) the "full
		// sync does not verify" flag, but with stock whatsmeow the failed full
		// sync already deleted the saved collection version, so the degraded
		// incremental path cannot engage until a primary recovery snapshot
		// restores local state. Later writes check the persisted flag before
		// starting another destructive full sync.
		if errors.Is(fetchErr, appstate.ErrMismatchingLTHash) || errors.Is(fetchErr, wa.ErrEmptyAppStateKeyShare) {
			return a.recoverMismatchingAppState(ctx, collection, markerGeneration, tracker, nil)
		}
		if !errors.Is(fetchErr, appstate.ErrKeyNotFound) {
			return fmt.Errorf("replay WhatsApp app state recovery for %s: %w", collection, fetchErr)
		}
		if err := waitForAppStateKeyDelivery(ctx, collection, &retryDelay); err != nil {
			return err
		}
	}
}

// waitForAppStateKeyDelivery waits before the next fetch of a collection
// whose patches could not be decoded for lack of a key. whatsmeow requests
// the missing keys when patch decoding returns ErrKeyNotFound; keep the
// connection alive for that delivery, with backoff and bounded by ctx so one
// absent key cannot hold all chat-state writes forever.
func waitForAppStateKeyDelivery(ctx context.Context, collection appstate.WAPatchName, retryDelay *time.Duration) error {
	timer := time.NewTimer(*retryDelay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return fmt.Errorf("wait for missing WhatsApp app state key during %s recovery: %w", collection, ctx.Err())
	case <-timer.C:
	}
	if *retryDelay < appStateRetryMaxDelay {
		*retryDelay = min(*retryDelay*2, appStateRetryMaxDelay)
	}
	return nil
}

// setAppStateFullSyncResult records whether the latest full sync of the
// collection verified. On accounts hitting persistent LTHash mismatches a full
// sync can never verify its snapshot, so the flag stays set until a full sync
// verifies again. The flag is mirrored durably in wacli.db: stock whatsmeow's
// full sync deletes the saved collection version before decoding, so the next
// process must know the collection is broken before it starts another
// destructive full sync.
func (a *App) setAppStateFullSyncResult(collection appstate.WAPatchName, broken bool) {
	a.appStateDegradedMu.Lock()
	a.loadAppStateFullSyncResultsLocked()
	if a.appStateFullSyncBroken == nil {
		a.appStateFullSyncBroken = make(map[string]bool)
	}
	changed := a.appStateFullSyncBroken[string(collection)] != broken
	a.appStateFullSyncBroken[string(collection)] = broken
	a.appStateDegradedMu.Unlock()
	if !changed {
		return
	}
	// Best-effort persistence: this process is still protected by the
	// in-memory flag, and the next failed full sync re-arms it.
	if err := a.db.SetAppStateFullSyncBroken(string(collection), broken); err != nil {
		a.warnAppStateRecovery(string(collection), fmt.Errorf("persist full sync verification flag: %w", err))
	}
}

// avoidsDestructiveFullSync reports whether replay debt of the collection is
// settled without a full sync (degraded pre-write path, startup): the write
// proceeds after an incremental fetch and the debt waits for the primary's
// snapshot. Stock whatsmeow deletes the saved collection version and MAC
// state before it decodes a full sync (it does not delete the contact store),
// so a snapshot that does not verify leaves the collection without local
// state until the primary answers. That applies to collections whose full
// sync is known not to verify, and always to `critical_unblock_low`: after a
// contact save the primary can rewrite contact entries this companion never
// stored (for example rows under a rotated-out key), which breaks the local
// hash, and stock whatsmeow cannot send the removal that would avoid it.
// Hash failures themselves are routed by keepsLocalAppStateOnRecovery.
func (a *App) avoidsDestructiveFullSync(collection appstate.WAPatchName) bool {
	return collection == appstate.WAPatchCriticalUnblockLow || a.isAppStateFullSyncBroken(collection)
}

// keepsLocalAppStateOnRecovery reports whether a hash failure of the
// collection must skip the full sync and wait for the primary's snapshot.
// Only `critical_unblock_low` with local state to lose: its local hash can
// stop verifying after a save (see avoidsDestructiveFullSync), and a full
// sync that then fails would delete the saved version, leaving the collection
// unreadable until the primary answers. Collections flagged by #497 keep
// main's behaviour here (a full replay first); changing that belongs to the
// follow-ups of #497, not to contacts. Without local state a full sync is the
// only way to get the collection and loses nothing.
func (a *App) keepsLocalAppStateOnRecovery(ctx context.Context, collection appstate.WAPatchName) bool {
	if collection != appstate.WAPatchCriticalUnblockLow {
		return false
	}
	hasLocal, err := a.wa.HasLocalAppState(ctx, string(collection))
	if err != nil {
		// Unknown: keep whatever is there rather than risk deleting it.
		a.warnAppStateRecovery(string(collection), fmt.Errorf("check local app state before recovery: %w", err))
		return true
	}
	return hasLocal
}

func (a *App) isAppStateFullSyncBroken(collection appstate.WAPatchName) bool {
	a.appStateDegradedMu.Lock()
	defer a.appStateDegradedMu.Unlock()
	a.loadAppStateFullSyncResultsLocked()
	return a.appStateFullSyncBroken[string(collection)]
}

// loadAppStateFullSyncResultsLocked hydrates the in-memory flags from
// wacli.db once per process. On a load failure the process simply behaves
// like today's main (full replay first) until a failed full sync re-arms the
// flag, so the error is not fatal.
func (a *App) loadAppStateFullSyncResultsLocked() {
	if a.appStateFullSyncLoaded {
		return
	}
	a.appStateFullSyncLoaded = true
	collections, err := a.db.AppStateFullSyncBrokenCollections()
	if err != nil {
		a.emitWarning("app_state_full_sync_flags_load_failed",
			fmt.Sprintf("warning: failed to load app state full sync verification flags: %v", err),
			map[string]any{"error": err.Error()})
		return
	}
	if a.appStateFullSyncBroken == nil {
		a.appStateFullSyncBroken = make(map[string]bool, len(collections))
	}
	for _, collection := range collections {
		a.appStateFullSyncBroken[collection] = true
	}
}

// degradedAppStateReplay handles pre-write replay debt on collections that
// avoid destructive full syncs (see avoidsDestructiveFullSync), such as those
// whose full sync is known not to verify. Replaying there repeats the very full sync
// that already failed — destroying the local collection version with stock
// whatsmeow — and then waits up to appStateRecoveryMaxWait for the primary,
// which is often asleep. Instead, with local state present, an incremental
// fetch from the local version confirms the server accepted our patches and
// the write proceeds. Only this attempt's own write-ahead marker is cleared:
// older intents can stand for mutations whatsmeow's cursor already consumed
// but wacli.db never persisted, and an empty incremental delta is not a
// replay of those, so that debt stays durable until a verified full replay or
// a primary recovery snapshot covers it (both clear through their own, newer
// generation). One background recovery request per collection per 6h still
// nudges the primary, since only it can repair the broken full sync. The
// contact collection takes the same path for debt a save left between its
// send and its mirror: the journal of that process died with it, so only a
// snapshot (one whatsmeow accepts, i.e. newer than the session's cursor) can
// replay the consumed batch.
//
// Returns handled=false (fall back to replayRequiredAppState, which waits for
// the primary's snapshot while local state exists and runs a full replay
// otherwise) when the collection does not avoid full syncs, there is no local
// state, or the incremental fetch fails.
func (a *App) degradedAppStateReplay(ctx context.Context, collection appstate.WAPatchName, markerGeneration int64, tracker *appStatePersistenceTracker) (handled bool, err error) {
	if !a.avoidsDestructiveFullSync(collection) {
		return false, nil
	}
	hasLocal, err := a.wa.HasLocalAppState(ctx, string(collection))
	if err != nil {
		a.warnAppStateRecovery(string(collection), fmt.Errorf("check local app state before degraded replay: %w", err))
		return false, nil
	}
	if !hasLocal {
		return false, nil
	}
	keyCtx, cancelKeyWait := context.WithTimeout(ctx, appStateRecoveryMaxWait)
	defer cancelKeyWait()
	retryDelay := appStateRetryInitialDelay
	var fetchErr error
	for {
		var persistenceErr error
		fetchErr, persistenceErr = a.fetchAndPersistAppState(keyCtx, collection, false, tracker)
		if persistenceErr != nil {
			return true, fmt.Errorf("persist degraded app state replay %s: %w", collection, persistenceErr)
		}
		if !errors.Is(fetchErr, appstate.ErrKeyNotFound) || collection != appstate.WAPatchCriticalUnblockLow {
			break
		}
		// The contact collection is never full-synced while local state
		// exists, and a snapshot is not how an ordinary missing key arrives:
		// retry the incremental fetch until whatsmeow receives the key.
		if err := waitForAppStateKeyDelivery(keyCtx, collection, &retryDelay); err != nil {
			return true, err
		}
	}
	if fetchErr != nil {
		// LTHash mismatch (or any other failure): today's behavior.
		return false, nil
	}
	reason := "full sync is known not to verify on this account"
	if collection == appstate.WAPatchCriticalUnblockLow {
		reason = "a contact batch was consumed without reaching wacli.db (a save exited between its send and its mirror, or a mirror failed); only the primary's snapshot can replay it"
	}
	a.emitWarning("app_state_degraded_replay",
		fmt.Sprintf("app state %s: %s; proceeding after an incremental sync and keeping the durable replay debt", collection, reason),
		map[string]any{"name": string(collection), "generation": markerGeneration})
	a.requestBackgroundAppStateRecovery(ctx, collection, markerGeneration)
	// Clear only this attempt's own write-ahead marker; see the function
	// comment for why older intents must survive an empty incremental delta.
	if err := a.db.ClearAppStateRecoveryIntent(string(collection), markerGeneration); err != nil {
		return true, fmt.Errorf("clear degraded replay marker for %s: %w", collection, err)
	}
	return true, nil
}

// requestBackgroundAppStateRecovery sends one recovery request to the primary
// without waiting for the answer, rate-limited per collection. whatsmeow
// adopts a late answer on its own when the snapshot is newer than the local
// state. The capture persists it in order and clears only covered replay debt.
func (a *App) requestBackgroundAppStateRecovery(ctx context.Context, collection appstate.WAPatchName, markerGeneration int64) {
	name := string(collection)
	now := time.Now()
	a.appStateDegradedMu.Lock()
	if last, ok := a.appStateRecoveryRequested[name]; ok && now.Sub(last) < backgroundAppStateRecoveryInterval {
		a.appStateDegradedMu.Unlock()
		return
	}
	if a.appStateRecoveryRequested == nil {
		a.appStateRecoveryRequested = make(map[string]time.Time)
	}
	a.appStateRecoveryRequested[name] = now
	a.appStateDegradedMu.Unlock()

	capture := a.newAppStateRecoveryCapture(collection)
	requestID, err := a.wa.RequestAppStateRecovery(ctx, name)
	if err != nil {
		capture.stop()
		a.warnAppStateRecovery(name, fmt.Errorf("background recovery request: %w", err))
		return
	}
	a.adoptLateAppStateRecovery(collection, markerGeneration, capture)
	a.emitWarning("app_state_recovery_requested_background",
		fmt.Sprintf("app state %s: requested recovery snapshot from the primary in the background (id %s)", name, requestID),
		map[string]any{"name": name, "id": string(requestID), "generation": markerGeneration})
}

func (a *App) recoverMismatchingAppState(ctx context.Context, collection appstate.WAPatchName, markerGeneration int64, tracker *appStatePersistenceTracker, onRequested func(types.MessageID)) error {
	ticket := a.appStatePersist.reserve()
	eventsToPersist, lateCapture, recoveryErr := a.waitForPrimaryAppStateRecovery(ctx, collection, onRequested)
	if lateCapture != nil {
		// The request reached the primary but the answer did not arrive in
		// time. Keep the capture alive in a background continuation so a late
		// answer still gets persisted and clears this attempt's markers.
		a.adoptLateAppStateRecovery(collection, markerGeneration, lateCapture)
	}
	persistCtx := context.WithoutCancel(ctx)
	result := make(chan error, 1)
	frontier := a.appStatePersist.complete(ticket, func() {
		if recoveryErr != nil {
			result <- nil
			return
		}
		result <- a.persistFetchedAppStateEvents(persistCtx, eventsToPersist, tracker)
	})
	if err := a.appStatePersist.waitThrough(persistCtx, frontier); err != nil {
		return err
	}
	if persistenceErr := <-result; persistenceErr != nil {
		return fmt.Errorf("persist recovered app state %s: %w", collection, persistenceErr)
	}
	if recoveryErr != nil {
		return recoveryErr
	}
	return a.clearCompletedAppStateRecovery(collection, markerGeneration)
}

// appStateRecoveryCapture buffers this collection's app state events until the
// recovery's AppStateSyncComplete sentinel arrives. Its event handler only
// appends to a slice and signals a buffered channel, so whatsmeow's dispatch
// never blocks on persistence.
type appStateRecoveryCapture struct {
	app        *App
	collection appstate.WAPatchName
	handlerID  uint32
	mu         sync.Mutex
	captured   []any
	finished   bool
	completed  chan appStateRecoveryCompletion
}

// appStateRecoveryCompletion carries the captured snapshot events together
// with the persistence ticket reserved at the dispatch boundary. Reserving
// inside the sentinel's event handler fixes the snapshot's position in the
// shared persistence order before whatsmeow can hand a newer live event to
// the persistence handler, so a late snapshot can never be replayed after a
// newer live mutation that overtook a sleeping continuation goroutine.
type appStateRecoveryCompletion struct {
	events []any
	ticket uint64
}

func (a *App) newAppStateRecoveryCapture(collection appstate.WAPatchName) *appStateRecoveryCapture {
	c := &appStateRecoveryCapture{app: a, collection: collection, completed: make(chan appStateRecoveryCompletion, 1)}
	// whatsmeow dispatches recovery mutations synchronously and emits this
	// collection's AppStateSyncComplete last, so the sentinel closes the drain.
	c.handlerID = a.wa.AddEventHandler(c.handle)
	return c
}

func (c *appStateRecoveryCapture) handle(evt any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	if syncComplete, ok := evt.(*events.AppStateSyncComplete); ok {
		if syncComplete != nil && syncComplete.Recovery && syncComplete.Name == c.collection {
			c.finished = true
			// Reserve the snapshot's ordered write position here, at the
			// dispatch boundary. reserve only increments a counter under a
			// mutex, so whatsmeow's dispatch never blocks on persistence.
			c.completed <- appStateRecoveryCompletion{
				events: append([]any(nil), c.captured...),
				ticket: c.app.appStatePersist.reserve(),
			}
		}
		return
	}
	if slices.Contains(appStateCollectionsForEvent(evt), c.collection) {
		c.captured = append(c.captured, evt)
	}
}

// stop finishes the capture and removes its event handler. Safe to call more
// than once. A completion nobody consumed still owns a persistence ticket
// reserved at the dispatch boundary; release it so later writes keep draining.
func (c *appStateRecoveryCapture) stop() {
	c.mu.Lock()
	c.finished = true
	c.mu.Unlock()
	c.app.wa.RemoveEventHandler(c.handlerID)
	select {
	case completion := <-c.completed:
		c.app.appStatePersist.complete(completion.ticket, func() {})
	default:
	}
}

// waitForPrimaryAppStateRecovery requests a recovery snapshot from the primary
// and waits for the captured events. When the wait times out after the request
// was sent, the still-registered capture is returned so the caller can hand it
// to a background continuation instead of dropping a late answer.
func (a *App) waitForPrimaryAppStateRecovery(ctx context.Context, collection appstate.WAPatchName, onRequested func(types.MessageID)) ([]any, *appStateRecoveryCapture, error) {
	capture := a.newAppStateRecoveryCapture(collection)
	requestID, err := a.wa.RequestAppStateRecovery(ctx, string(collection))
	if err != nil {
		capture.stop()
		return nil, nil, fmt.Errorf("request WhatsApp app state recovery for %s: %w", collection, err)
	}
	if onRequested != nil {
		onRequested(requestID)
	}
	select {
	case completion := <-capture.completed:
		capture.stop()
		// The timely waiter persists under the ticket its caller reserved
		// before the request was sent; release the dispatch-boundary ticket
		// without work so the queue keeps draining.
		a.appStatePersist.complete(completion.ticket, func() {})
		return completion.events, nil, nil
	case <-ctx.Done():
		return nil, capture, fmt.Errorf("wait for WhatsApp app state recovery for %s: %w", collection, ctx.Err())
	}
}

// lateAppStateRecovery is a background continuation of a recovery attempt
// whose synchronous waiter timed out after the request was sent.
type lateAppStateRecovery struct {
	capture    *appStateRecoveryCapture
	generation int64
	superseded chan struct{}
}

// adoptLateAppStateRecovery registers at most one continuation per collection;
// a newer attempt's higher generation supersedes an older one. The capture is
// dropped when the app is closing or an equal-or-newer continuation exists.
func (a *App) adoptLateAppStateRecovery(collection appstate.WAPatchName, generation int64, capture *appStateRecoveryCapture) {
	a.appStateRecoveryMu.Lock()
	if a.appStateRecoveryClosing {
		a.appStateRecoveryMu.Unlock()
		capture.stop()
		return
	}
	if existing := a.lateRecoveries[string(collection)]; existing != nil {
		if existing.generation >= generation {
			a.appStateRecoveryMu.Unlock()
			capture.stop()
			return
		}
		close(existing.superseded)
		delete(a.lateRecoveries, string(collection))
	}
	if a.lateRecoveries == nil {
		a.lateRecoveries = make(map[string]*lateAppStateRecovery)
	}
	if a.lateRecoveryStop == nil {
		a.lateRecoveryStop = make(chan struct{})
	}
	cont := &lateAppStateRecovery{capture: capture, generation: generation, superseded: make(chan struct{})}
	a.lateRecoveries[string(collection)] = cont
	stop := a.lateRecoveryStop
	a.appStateRecoveryWorkers.Go(func() {
		a.runLateAppStateRecovery(collection, cont, stop)
	})
	a.appStateRecoveryMu.Unlock()
}

func (a *App) runLateAppStateRecovery(collection appstate.WAPatchName, cont *lateAppStateRecovery, stop <-chan struct{}) {
	timer := time.NewTimer(lateAppStateRecoveryMaxWait)
	defer timer.Stop()
	var completion appStateRecoveryCompletion
	select {
	case completion = <-cont.capture.completed:
	case <-cont.superseded:
		// The superseding attempt already removed this entry from the registry.
		cont.capture.stop()
		return
	case <-stop:
		a.finishLateAppStateRecovery(collection, cont)
		return
	case <-timer.C:
		a.finishLateAppStateRecovery(collection, cont)
		return
	}
	a.finishLateAppStateRecovery(collection, cont)
	// Same ordered persistence as the timely path. The write position was
	// reserved when the sentinel was dispatched, so a newer live event that
	// arrived after the snapshot persists after it, even though this goroutine
	// runs outside whatsmeow's dispatch and holds no chatStateSync.
	persistCtx := context.Background()
	tracker := &appStatePersistenceTracker{}
	result := make(chan error, 1)
	frontier := a.appStatePersist.complete(completion.ticket, func() {
		result <- a.persistFetchedAppStateEvents(persistCtx, completion.events, tracker)
	})
	if err := a.appStatePersist.waitThrough(persistCtx, frontier); err != nil {
		a.warnAppStateRecovery(string(collection), err)
		return
	}
	if persistenceErr := <-result; persistenceErr != nil {
		a.warnAppStateRecovery(string(collection), fmt.Errorf("persist late recovered app state %s: %w", collection, persistenceErr))
		return
	}
	if err := a.clearCompletedAppStateRecovery(collection, cont.generation); err != nil {
		a.warnAppStateRecovery(string(collection), err)
		return
	}
	a.emitWarning("app_state_recovery_completed_late",
		fmt.Sprintf("App state %s recovered late from primary", collection),
		map[string]any{"name": string(collection), "generation": cont.generation})
}

func (a *App) finishLateAppStateRecovery(collection appstate.WAPatchName, cont *lateAppStateRecovery) {
	cont.capture.stop()
	a.appStateRecoveryMu.Lock()
	if a.lateRecoveries[string(collection)] == cont {
		delete(a.lateRecoveries, string(collection))
	}
	a.appStateRecoveryMu.Unlock()
}

func (a *App) fetchAndPersistAppState(ctx context.Context, collection appstate.WAPatchName, fullSync bool, tracker *appStatePersistenceTracker) (fetchErr, persistenceErr error) {
	ticket := a.appStatePersist.reserve()
	var eventsToPersist []any
	func() {
		releaseFetch := a.beginManualAppStateFetch(collection)
		defer releaseFetch()
		eventsToPersist, fetchErr = a.wa.FetchAppStateEvents(ctx, string(collection), fullSync, false)
	}()
	// Track whether full syncs of this collection verify. Set on an LTHash
	// mismatch, cleared when a full sync verifies again.
	if fullSync {
		if fetchErr == nil {
			a.setAppStateFullSyncResult(collection, false)
		} else if errors.Is(fetchErr, appstate.ErrMismatchingLTHash) {
			a.setAppStateFullSyncResult(collection, true)
		}
	}
	result := make(chan error, 1)
	frontier := a.appStatePersist.complete(ticket, func() {
		persistErr := a.persistFetchedAppStateEvents(ctx, eventsToPersist, tracker)
		if collection == appstate.WAPatchCriticalUnblockLow {
			// Contact writes journaled while no handler was listening (before
			// the handlers were installed, or by a failed earlier drain) are
			// mirrored with this fetch's own.
			_, drainErr := a.drainContactJournal(ctx)
			persistErr = errors.Join(persistErr, drainErr)
		}
		result <- persistErr
	})
	if waitErr := a.appStatePersist.waitThrough(ctx, frontier); waitErr != nil {
		return fetchErr, waitErr
	}
	return fetchErr, <-result
}

func (a *App) beginManualAppStateFetch(collection appstate.WAPatchName) func() {
	name := string(collection)
	a.manualFetchMu.Lock()
	if a.manualFetches == nil {
		a.manualFetches = make(map[string]int)
	}
	a.manualFetches[name]++
	a.manualFetchMu.Unlock()
	return func() {
		a.manualFetchMu.Lock()
		a.manualFetches[name]--
		if a.manualFetches[name] == 0 {
			delete(a.manualFetches, name)
		}
		a.manualFetchMu.Unlock()
	}
}

func (a *App) ownsManualAppStateFetch(collection appstate.WAPatchName) bool {
	a.manualFetchMu.Lock()
	defer a.manualFetchMu.Unlock()
	return a.manualFetches[string(collection)] > 0
}

func (a *App) clearCompletedAppStateRecovery(collection appstate.WAPatchName, markerGeneration int64) error {
	cleared, err := a.db.ClearAppStateRecoveryGeneration(string(collection), markerGeneration)
	if err != nil {
		return fmt.Errorf("clear WhatsApp app state recovery for %s: %w", collection, err)
	}
	if !cleared {
		required, checkErr := a.db.AppStateRecoveryRequired(string(collection))
		if checkErr != nil {
			return fmt.Errorf("check completed WhatsApp app state recovery for %s: %w", collection, checkErr)
		}
		if required {
			return fmt.Errorf("WhatsApp app state recovery changed during %s synchronization; retry the command", collection)
		}
	}
	return nil
}

type pendingLocalAppStateWrite struct {
	collection appstate.WAPatchName
	generation int64
	once       sync.Once
	ticket     uint64
	reserved   bool
}

func (p *pendingLocalAppStateWrite) reserve(a *App) {
	p.once.Do(func() {
		p.ticket = a.appStatePersist.reserve()
		p.reserved = true
	})
}

func (a *App) beginLocalAppStateWrite(collection appstate.WAPatchName) (pendingLocalAppStateWrite, error) {
	generation, err := a.db.MarkAppStateRecoveryGeneration(string(collection))
	if err != nil {
		return pendingLocalAppStateWrite{}, fmt.Errorf("mark local WhatsApp app state recovery for %s: %w", collection, err)
	}
	// Keep this exact intent until a later pre-write replay. Other queued work
	// clears only its own generation and cannot erase an unfinished send.
	return pendingLocalAppStateWrite{collection: collection, generation: generation}, nil
}

func (a *App) failLocalAppStateWrite(ctx context.Context, pending *pendingLocalAppStateWrite, postSendEvents []any) error {
	if !pending.reserved {
		return nil
	}
	result := make(chan error, 1)
	persistCtx := context.WithoutCancel(ctx)
	frontier := a.appStatePersist.complete(pending.ticket, func() {
		tracker := &appStatePersistenceTracker{}
		result <- a.persistFetchedAppStateEvents(persistCtx, postSendEvents, tracker)
	})
	if err := a.appStatePersist.waitThrough(persistCtx, frontier); err != nil {
		return err
	}
	return <-result
}

func (a *App) completeLocalAppStateWrite(ctx context.Context, pending *pendingLocalAppStateWrite, postSendEvents []any, persist func() error) error {
	result := make(chan error, 1)
	persistCtx := context.WithoutCancel(ctx)
	frontier := a.appStatePersist.complete(pending.ticket, func() {
		localErr := persist()
		tracker := &appStatePersistenceTracker{}
		eventsErr := a.persistFetchedAppStateEvents(persistCtx, postSendEvents, tracker)
		persistErr := localErr
		if persistErr == nil {
			persistErr = eventsErr
		}
		result <- persistErr
	})
	if err := a.appStatePersist.waitThrough(persistCtx, frontier); err != nil {
		return err
	}
	// Keep this write's intent durable even though persistErr == nil: pinned
	// whatsmeow's SendAppState advances the collection cursor and dispatches
	// the post-send fetched mutations in a goroutine after returning, so a nil
	// persistErr only proves the explicit local update and already-admitted
	// tasks completed — not that the SDK's complete post-send batch reached
	// wacli.db. Deleting the intent here would leave no marker if the process
	// exits before that goroutine runs, and future incremental deltas would
	// skip the batch forever. On broken collections the degraded pre-write
	// path keeps later writes fast despite the kept debt; a verified full
	// replay or primary recovery snapshot clears it. A contact save settles
	// its own intent after projecting the journal of whatsmeow's contact
	// store writes, which holds the post-send batch before SendAppState
	// returns (see SaveContact).
	return <-result
}

func (a *App) persistFetchedAppStateEvents(ctx context.Context, eventsToPersist []any, tracker *appStatePersistenceTracker) error {
	tracker.begin()
	for _, evt := range eventsToPersist {
		a.persistAppStateEvent(ctx, evt, tracker)
	}
	return tracker.end()
}

func (a *App) latestMessageRange(chatJID string) (time.Time, *waCommon.MessageKey) {
	info, err := a.db.GetLatestMessageInfo(chatJID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.emitWarning(
				"chat_state_latest_message_failed",
				fmt.Sprintf("warning: failed to load latest message for chat state patch: %v", err),
				map[string]any{"chat_jid": chatJID, "error": err.Error()},
			)
		}
		return time.Time{}, nil
	}
	return info.Timestamp, messageKeyFromStore(info)
}

func messageKeyFromStore(info store.MessageInfo) *waCommon.MessageKey {
	if strings.TrimSpace(info.ChatJID) == "" || strings.TrimSpace(info.MsgID) == "" {
		return nil
	}
	key := &waCommon.MessageKey{
		RemoteJID: proto.String(info.ChatJID),
		FromMe:    proto.Bool(info.FromMe),
		ID:        proto.String(info.MsgID),
	}
	if sender := strings.TrimSpace(info.SenderJID); sender != "" && sender != info.ChatJID {
		key.Participant = proto.String(sender)
	}
	return key
}

func (a *App) handleChatStateEvent(ctx context.Context, evt any) error {
	switch v := evt.(type) {
	case *events.Archive:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.ApplyChatArchive(canonicalJIDString(chat), v.Action.GetArchived(), v.Timestamp); err != nil {
			a.emitChatStateWarning("archive", v.JID, err)
			return err
		}
	case *events.UnarchiveChatsSetting:
		if v == nil || v.Action == nil || v.Action.UnarchiveChats == nil {
			return nil
		}
		return a.db.SetChatArchiveSetting(v.Action.GetUnarchiveChats(), v.Timestamp)
	case *events.Pin:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatPinned(canonicalJIDString(chat), v.Action.GetPinned()); err != nil {
			a.emitChatStateWarning("pin", v.JID, err)
			return err
		}
	case *events.Mute:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		if err := a.db.SetChatMutedUntil(canonicalJIDString(chat), mutedUntilFromAction(v.Action)); err != nil {
			a.emitChatStateWarning("mute", v.JID, err)
			return err
		}
	case *events.MarkChatAsRead:
		if v == nil || v.JID.IsEmpty() || v.Action == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		var err error
		if v.Action.GetRead() {
			through, ids := markChatAsReadPosition(v)
			err = a.clearChatUnreadFromEvent(canonicalJIDString(chat), through, ids)
		} else {
			err = a.db.SetChatUnread(canonicalJIDString(chat), true)
		}
		if err != nil {
			a.emitChatStateWarning("mark_read", v.JID, err)
			return err
		}
	}
	return nil
}

// handleContactEvent mirrors whatsmeow's contact store writes into wacli.db
// when a `contact` app-state event arrives (the echo of our own `contacts
// save`, or an edit made on the phone or another linked device), so
// `contacts show` matches the session without a `contacts refresh`. The event
// only triggers a drain of the journal (see drainContactJournal); its payload
// is never applied. whatsmeow writes its store synchronously while it applies
// a batch and dispatches each batch's events from its own goroutine, so a
// delayed or duplicated event carries no usable order, while the journal
// holds the SDK's application order across a contact's phone-number and LID
// indices, its clears, and the writes whose store update failed.
func (a *App) handleContactEvent(ctx context.Context, _ *events.Contact) error {
	_, err := a.drainContactJournal(ctx)
	return err
}

func mutedUntilFromAction(action *waSyncAction.MuteAction) int64 {
	if action == nil || !action.GetMuted() {
		return 0
	}
	ms := action.GetMuteEndTimestamp()
	if ms < 0 {
		return -1
	}
	if ms > 0 {
		return time.UnixMilli(ms).Unix()
	}
	return -1
}

func mutedUntilUnix(mute bool, duration time.Duration, base time.Time) int64 {
	if !mute {
		return 0
	}
	if duration <= 0 {
		return -1
	}
	return base.Add(duration).Unix()
}

func (a *App) emitChatStateWarning(kind string, jid types.JID, err error) {
	a.emitWarning(
		"chat_state_store_failed",
		fmt.Sprintf("warning: failed to store %s chat state for %s: %v", kind, jid, err),
		map[string]any{"kind": kind, "jid": jid.String(), "error": err.Error()},
	)
}
