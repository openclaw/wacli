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
	if evt == nil || !errors.Is(evt.Error, appstate.ErrMismatchingLTHash) {
		return
	}
	a.startAppStateRecovery(ctx, evt.Name, recoveries, "app_state_lthash_mismatch", "hit an LTHash mismatch")
}

// handleAppStateKeyUnavailable reacts to the primary answering a key request
// without key data. whatsmeow only knows how to ask for the key again, which
// returns the same empty share, so every collection already failing on a
// missing key goes to the full sync -> recovery snapshot path instead.
func (a *App) handleAppStateKeyUnavailable(ctx context.Context, evt *wa.AppStateKeyUnavailable, recoveries *sync.Map) {
	if evt == nil {
		return
	}
	a.appStateKeyMu.Lock()
	a.appStateKeyUnavailable = true
	names := make([]string, 0, len(a.appStateKeyMissing))
	for name := range a.appStateKeyMissing {
		names = append(names, name)
	}
	a.appStateKeyMu.Unlock()
	slices.Sort(names)

	keyID := fmt.Sprintf("%X", evt.KeyID)
	a.emitWarning("app_state_key_unavailable",
		fmt.Sprintf("warning: primary device has no data for app state key %s; collections that need it will be recovered from a snapshot", keyID),
		map[string]any{"key_id": keyID})
	for _, name := range names {
		a.startAppStateRecovery(ctx, appstate.WAPatchName(name), recoveries, "app_state_key_missing", "needs a key the primary device no longer has")
	}
}

// noteAppStateKeyMissing records a collection that failed on ErrKeyNotFound and
// reports whether the primary already said it cannot supply a missing key.
func (a *App) noteAppStateKeyMissing(name appstate.WAPatchName) (primaryLacksKey bool) {
	a.appStateKeyMu.Lock()
	defer a.appStateKeyMu.Unlock()
	if a.appStateKeyMissing == nil {
		a.appStateKeyMissing = make(map[string]struct{})
	}
	a.appStateKeyMissing[string(name)] = struct{}{}
	return a.appStateKeyUnavailable
}

func (a *App) primaryLacksAppStateKey() bool {
	a.appStateKeyMu.Lock()
	defer a.appStateKeyMu.Unlock()
	return a.appStateKeyUnavailable
}

func (a *App) startAppStateRecovery(ctx context.Context, collection appstate.WAPatchName, recoveries *sync.Map, warningCode, reason string) {
	if a.ownsManualAppStateFetch(collection) {
		return
	}
	name := strings.TrimSpace(string(collection))
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
		a.emitWarning(warningCode,
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
	for _, name := range pending {
		if _, loaded := recoveries.LoadOrStore(name, struct{}{}); !loaded {
			a.recoverAppStateCollection(ctx, name, recoveries, appStateRecoveryStepTimeout)
		}
	}
	for _, name := range []appstate.WAPatchName{appstate.WAPatchRegularHigh, appstate.WAPatchRegularLow, appstate.WAPatchRegular} {
		if _, recovering := recoveries.Load(string(name)); recovering {
			continue
		}
		fullSync := name == appstate.WAPatchRegular
		if err := a.wa.FetchAppState(ctx, string(name), fullSync, false); err != nil {
			if errors.Is(err, appstate.ErrKeyNotFound) && a.noteAppStateKeyMissing(name) {
				a.startAppStateRecovery(ctx, name, recoveries, "app_state_key_missing", "needs a key the primary device no longer has")
				continue
			}
			a.emitWarning("app_state_sync_failed",
				fmt.Sprintf("warning: failed to sync WhatsApp app state %s: %v", name, err),
				map[string]any{"name": string(name), "error": err.Error()})
		}
	}
}
