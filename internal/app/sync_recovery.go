package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const appStateRecoveryStepTimeout = 30 * time.Second

func (a *App) handleAppStateSyncError(ctx context.Context, evt *events.AppStateSyncError, recoveries *sync.Map) {
	if evt == nil {
		return
	}
	code, reason := "app_state_lthash_mismatch", "hit an LTHash mismatch"
	if errors.Is(evt.Error, wa.ErrEmptyAppStateKeyShare) {
		code, reason = "app_state_key_unavailable", "requires a key shared without data"
	} else if !errors.Is(evt.Error, appstate.ErrMismatchingLTHash) {
		return
	}
	if a.ownsManualAppStateFetch(evt.Name) {
		return
	}
	name := strings.TrimSpace(string(evt.Name))
	if name == "" {
		return
	}
	if recoveries == nil {
		recoveries = &sync.Map{}
	}
	a.appStateRecoveryMu.Lock()
	defer a.appStateRecoveryMu.Unlock()
	if a.appStateRecoveryClosing {
		return
	}
	if _, loaded := recoveries.LoadOrStore(name, struct{}{}); loaded {
		return
	}

	a.appStateRecoveryWorkers.Go(func() {
		a.emitWarning(code,
			fmt.Sprintf("warning: app state %s %s; attempting full sync", name, reason),
			map[string]any{"name": name})
		a.recoverAppStateCollection(ctx, name, recoveries, appStateRecoveryStepTimeout)
	})
}

func (a *App) recoverAppStateCollection(ctx context.Context, name string, recoveries *sync.Map, timeout time.Duration) {
	defer func() {
		if ctx.Err() != nil {
			recoveries.Delete(name)
		}
	}()
	lockCtx, cancelLock := context.WithTimeout(ctx, timeout)
	release, err := a.acquireChatStateSync(lockCtx)
	cancelLock()
	if err != nil {
		a.warnAppStateRecovery(name, err)
		return
	}
	defer release()

	generation, _, err := a.db.BeginAppStateRecovery(name)
	if err != nil {
		a.warnAppStateRecovery(name, err)
		return
	}
	collection := appstate.WAPatchName(name)
	tracker := &appStatePersistenceTracker{}
	fetchCtx, cancelFetch := context.WithTimeout(ctx, timeout)
	fetchErr, persistenceErr := a.fetchAndPersistAppState(fetchCtx, collection, true, tracker)
	cancelFetch()
	if persistenceErr != nil {
		a.warnAppStateRecovery(name, fmt.Errorf("persist full app state replay: %w", persistenceErr))
		return
	}
	if fetchErr == nil {
		if err := a.clearCompletedAppStateRecovery(collection, generation); err != nil {
			a.warnAppStateRecovery(name, err)
			return
		}
		a.emitOrPrint("app_state_full_sync_completed", map[string]any{"name": name},
			"\rApp state %s resolved via full sync\n", name)
		return
	}
	if ctx.Err() != nil {
		return
	}
	a.emitWarning("app_state_full_sync_failed",
		fmt.Sprintf("warning: app state %s full sync failed: %v; requesting recovery snapshot", name, fetchErr),
		map[string]any{"name": name, "error": fetchErr.Error()})

	// A full-fetch timeout must not consume the primary recovery budget.
	recoveryCtx, cancelRecovery := context.WithTimeout(ctx, timeout)
	defer cancelRecovery()
	err = a.recoverMismatchingAppState(recoveryCtx, collection, generation, tracker, func(id types.MessageID) {
		if a.eventsEnabled() {
			a.emitEvent("app_state_recovery_requested", map[string]any{"name": name, "id": string(id)})
		} else {
			fmt.Fprintf(os.Stderr, "\rRequested app state %s recovery (id %s)\n", name, id)
		}
	})
	if err != nil {
		a.warnAppStateRecovery(name, err)
	}
}

func (a *App) warnAppStateRecovery(name string, err error) {
	a.emitWarning("app_state_recovery_failed",
		fmt.Sprintf("warning: app state %s recovery failed: %v", name, err),
		map[string]any{"name": name, "error": err.Error()})
}

func (a *App) syncAppStateDeltas(ctx context.Context, recoveries *sync.Map) {
	pending, err := a.db.AppStateRecoveryCollections()
	if err != nil {
		a.emitWarning("app_state_sync_failed", fmt.Sprintf("warning: cannot inspect app state recovery: %v", err),
			map[string]any{"error": err.Error()})
		return
	}
	a.requestChatArchiveRecovery(ctx, pending)
	for _, name := range pending {
		if _, loaded := recoveries.LoadOrStore(name, struct{}{}); !loaded {
			collection := appstate.WAPatchName(name)
			if a.isAppStateFullSyncBroken(collection) {
				// A snapshot must cover replay debt, even when the preference
				// was already seen. Never destroy a known-broken collection.
				generation, _, err := a.db.BeginAppStateRecovery(name)
				if err != nil {
					a.warnAppStateRecovery(name, err)
					continue
				}
				a.requestBackgroundAppStateRecovery(ctx, collection, generation)
				// Keep catching up new mutations while the primary is asleep.
				// This does not settle the older, still-unseen snapshot debt.
				fetchErr, persistenceErr := a.fetchAndPersistAppState(ctx, collection, false, &appStatePersistenceTracker{})
				if err := errors.Join(fetchErr, persistenceErr); err != nil {
					a.warnAppStateRecovery(name, err)
				}
				continue
			}
			a.recoverAppStateCollection(ctx, name, recoveries, appStateRecoveryStepTimeout)
		}
	}
	for _, name := range []appstate.WAPatchName{appstate.WAPatchRegularHigh, appstate.WAPatchRegularLow, appstate.WAPatchRegular} {
		if _, recovering := recoveries.Load(string(name)); recovering {
			continue
		}
		fullSync := name == appstate.WAPatchRegular
		if err := a.wa.FetchAppState(ctx, string(name), fullSync, false); err != nil {
			if errors.Is(err, wa.ErrEmptyAppStateKeyShare) {
				a.handleAppStateSyncError(ctx, &events.AppStateSyncError{Name: name, FullSync: fullSync, Error: err}, recoveries)
				continue
			}
			a.emitWarning("app_state_sync_failed",
				fmt.Sprintf("warning: failed to sync WhatsApp app state %s: %v", name, err),
				map[string]any{"name": string(name), "error": err.Error()})
		}
	}
}

// requestChatArchiveRecovery recovers the Keep chats archived preference and
// the archive timestamps that older builds consumed without saving. Incremental
// deltas cannot supply them, and a full regular_low replay is not a safe first
// resort on an upgraded store: stock whatsmeow deletes the saved collection
// version before decoding a full sync, so when the server's snapshot does not
// verify the device is left without usable local state until the primary
// answers, and the primary's answer is only adopted when it is strictly newer
// than what is left. Ask the primary for its snapshot in the background
// instead, rate-limited per collection, and keep syncing incrementally. The
// snapshot's completion sentinel settles the recovery through the ordered
// persistence path; until then the metadata stays unknown, so archived chats
// stay archived and nothing auto-unarchives. No replay intent is recorded, so
// the next chat-state write is not sent through a full replay either.
//
// When regular_low already carries replay debt, that debt's own recovery (a
// verified full replay or the primary's snapshot) ends with the same sentinel
// and settles the archive metadata too, so no separate request is needed and
// the replay-debt path is left unchanged.
//
// Nothing short of a complete snapshot settles it. If the primary never
// answers, the metadata stays unknown: archived chats stay archived, nothing
// auto-unarchives, and the background request repeats at most once per
// backgroundAppStateRecoveryInterval.
func (a *App) requestChatArchiveRecovery(ctx context.Context, pending []string) {
	required, err := a.db.ChatArchiveRecoveryRequired()
	if err != nil {
		a.warnAppStateRecovery(string(appstate.WAPatchRegularLow), err)
		return
	}
	if !required || slices.Contains(pending, string(appstate.WAPatchRegularLow)) {
		return
	}
	// Generation 0 owns no intent: the snapshot clears no replay debt, and
	// intents recorded while it is pending stay until their own recovery.
	a.requestBackgroundAppStateRecovery(ctx, appstate.WAPatchRegularLow, 0)
}

func (a *App) warnEmptyAppStateKey(evt *wa.AppStateKeyUnavailable) {
	if evt == nil {
		return
	}
	keyID := fmt.Sprintf("%X", evt.KeyID)
	a.emitWarning("app_state_key_unavailable",
		fmt.Sprintf("warning: primary device shared app state key %s without data; collections that cannot read it will attempt snapshot recovery", keyID),
		map[string]any{"key_id": keyID})
}
