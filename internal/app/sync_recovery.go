package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const appStateRecoveryStepTimeout = 30 * time.Second

func (a *App) handleAppStateSyncError(ctx context.Context, evt *events.AppStateSyncError, recoveries *sync.Map) {
	if evt == nil || !errors.Is(evt.Error, appstate.ErrMismatchingLTHash) {
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
		a.emitWarning("app_state_lthash_mismatch",
			fmt.Sprintf("warning: app state %s hit an LTHash mismatch; attempting full sync", name),
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
			a.emitWarning("app_state_sync_failed",
				fmt.Sprintf("warning: failed to sync WhatsApp app state %s: %v", name, err),
				map[string]any{"name": string(name), "error": err.Error()})
		}
	}
}
