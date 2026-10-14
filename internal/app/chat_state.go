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
			*events.Archive, *events.Pin, *events.Mute, *events.MarkChatAsRead:
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
	postSendEvents, err := a.wa.ArchiveChat(ctx, jid, archive, nowUTC(), nil, func() { pending.reserve(a) })
	if err != nil {
		return errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	return a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		return a.db.SetChatArchived(chatJID, archive)
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
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		return err
	}
	defer release()
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	lastTS, lastKey := a.latestMessageRange(chatJID)
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
		return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
	}

	for {
		err, persistenceErr := a.fetchAndPersistAppState(ctx, collection, false, tracker)
		if persistenceErr != nil {
			return fmt.Errorf("persist fetched app state %s: %w", collection, persistenceErr)
		}
		if err == nil {
			return a.clearCompletedAppStateRecovery(collection, markerGeneration)
		}

		if errors.Is(err, appstate.ErrMismatchingLTHash) || errors.Is(err, wa.ErrEmptyAppStateKeyShare) {
			return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
		} else if errors.Is(err, appstate.ErrKeyNotFound) {
			return a.replayRequiredAppState(ctx, collection, markerGeneration, tracker)
		} else {
			return fmt.Errorf("sync WhatsApp chat state before update: %w", err)
		}
	}
}

func (a *App) replayRequiredAppState(ctx context.Context, collection appstate.WAPatchName, markerGeneration int64, tracker *appStatePersistenceTracker) error {
	ctx, cancel := context.WithTimeout(ctx, appStateRecoveryMaxWait)
	defer cancel()
	retryDelay := appStateRetryInitialDelay
	for {
		fetchErr, persistenceErr := a.fetchAndPersistAppState(ctx, collection, true, tracker)
		if persistenceErr != nil {
			return fmt.Errorf("persist replayed app state %s: %w", collection, persistenceErr)
		}
		if fetchErr == nil {
			return a.clearCompletedAppStateRecovery(collection, markerGeneration)
		}
		if errors.Is(fetchErr, appstate.ErrMismatchingLTHash) || errors.Is(fetchErr, wa.ErrEmptyAppStateKeyShare) {
			return a.recoverMismatchingAppState(ctx, collection, markerGeneration, tracker, nil)
		}
		if !errors.Is(fetchErr, appstate.ErrKeyNotFound) {
			return fmt.Errorf("replay WhatsApp app state recovery for %s: %w", collection, fetchErr)
		}
		// whatsmeow requests the missing keys when patch decoding returns
		// ErrKeyNotFound. Keep the connection alive for that delivery, bounded
		// so one absent key cannot hold all chat-state writes forever.

		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for missing WhatsApp app state key during %s recovery: %w", collection, ctx.Err())
		case <-timer.C:
		}
		if retryDelay < appStateRetryMaxDelay {
			retryDelay *= 2
			if retryDelay > appStateRetryMaxDelay {
				retryDelay = appStateRetryMaxDelay
			}
		}
	}
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
	result := make(chan error, 1)
	frontier := a.appStatePersist.complete(ticket, func() {
		result <- a.persistFetchedAppStateEvents(ctx, eventsToPersist, tracker)
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
	// Another whatsmeow fetch can advance the cursor and dispatch later. Keep
	// replay debt durable until the next pre-write full replay.
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
		if err := a.db.SetChatArchived(canonicalJIDString(chat), v.Action.GetArchived()); err != nil {
			a.emitChatStateWarning("archive", v.JID, err)
			return err
		}
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
			err = a.db.ClearChatUnreadThrough(canonicalJIDString(chat), through, ids)
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
