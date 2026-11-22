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

type AppStateResyncResult struct {
	Collection   string `json:"collection"`
	Completeness string `json:"completeness"`
	QuickReplies int    `json:"quick_replies"`
	Lists        int    `json:"lists"`
}

// ResyncAppState forces the existing replay/recovery path, without a write.
// Stock whatsmeow drops the saved collection version before verifying a full
// sync; known-broken collections must be repaired by primary recovery instead.
func (a *App) ResyncAppState(ctx context.Context, name string) (AppStateResyncResult, error) {
	if name != string(appstate.WAPatchRegular) {
		return AppStateResyncResult{}, fmt.Errorf("app-state resync supports only regular")
	}
	release, err := a.acquireChatStateSync(ctx)
	if err != nil {
		return AppStateResyncResult{}, err
	}
	defer release()
	broken, err := a.db.AppStateFullSyncBrokenCollections()
	if err != nil {
		return AppStateResyncResult{}, err
	}
	if slices.Contains(broken, name) || a.isAppStateFullSyncBroken(appstate.WAPatchRegular) {
		return AppStateResyncResult{}, fmt.Errorf("app state %s is flagged app_state_full_sync_broken; refusing a full sync that can discard usable local state before verification; use `wacli sync --follow` for primary recovery instead", name)
	}
	generation, _, err := a.db.BeginAppStateRecovery(name)
	if err != nil {
		return AppStateResyncResult{}, err
	}
	if err := a.replayRequiredAppState(ctx, appstate.WAPatchRegular, generation, &appStatePersistenceTracker{}); err != nil {
		return AppStateResyncResult{}, err
	}
	replies, err := a.db.QuickReplies()
	if err != nil {
		return AppStateResyncResult{}, err
	}
	lists, err := a.db.Lists()
	if err != nil {
		return AppStateResyncResult{}, err
	}
	return AppStateResyncResult{Collection: name, Completeness: replies.Completeness, QuickReplies: len(replies.QuickReplies), Lists: len(lists.Lists)}, nil
}

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
	// Older builds consumed this preference without saving it. Incremental
	// deltas cannot recover it, so request the existing full-replay path.
	archiveRecovery, err := a.db.ChatArchiveRecoveryRequired()
	if err != nil {
		a.warnAppStateRecovery(string(appstate.WAPatchRegularLow), err)
		return
	}
	if archiveRecovery {
		if err := a.db.MarkAppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil {
			a.warnAppStateRecovery(string(appstate.WAPatchRegularLow), err)
			return
		}
	}
	pending, err := a.db.AppStateRecoveryCollections()
	if err != nil {
		a.emitWarning("app_state_sync_failed", fmt.Sprintf("warning: cannot inspect app state recovery: %v", err),
			map[string]any{"error": err.Error()})
		return
	}
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

func (a *App) warnEmptyAppStateKey(evt *wa.AppStateKeyUnavailable) {
	if evt == nil {
		return
	}
	keyID := fmt.Sprintf("%X", evt.KeyID)
	a.emitWarning("app_state_key_unavailable",
		fmt.Sprintf("warning: primary device shared app state key %s without data; collections that cannot read it will attempt snapshot recovery", keyID),
		map[string]any{"key_id": keyID})
}
