package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

// degradedTestApp builds an app whose WA fake can distinguish full and
// incremental syncs, for the degraded (full-sync-known-broken) write path.
func degradedTestApp(t *testing.T, chat types.JID) (*App, *appStateContextWA) {
	t.Helper()
	a := newTestApp(t)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	return a, f
}

// forceBrokenFullSync drives one failing full sync through the production
// funnel (fetchAndPersistAppState) so the collection gets flagged as
// "full sync known not to verify", then restores the previous fetch hook.
func forceBrokenFullSync(t *testing.T, a *App, f *appStateContextWA, collection appstate.WAPatchName) {
	t.Helper()
	prev := f.fetchEvents
	f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) {
		return nil, appstate.ErrMismatchingLTHash
	}
	fetchErr, persistErr := a.fetchAndPersistAppState(t.Context(), collection, true, &appStatePersistenceTracker{})
	if persistErr != nil {
		t.Fatalf("persist during forced full sync: %v", persistErr)
	}
	if !errors.Is(fetchErr, appstate.ErrMismatchingLTHash) {
		t.Fatalf("forced full sync error = %v, want ErrMismatchingLTHash", fetchErr)
	}
	f.fetchEvents = prev
	if !a.isAppStateFullSyncBroken(collection) {
		t.Fatal("failing full sync did not flag the collection")
	}
}

// (a) On a flagged collection, a successful local write whose post-send
// persistence succeeded leaves no replay debt behind.
func TestDegradedCollectionWriteKeepsNoMarker(t *testing.T) {
	chat := types.JID{User: "15550000020", Server: types.DefaultUserServer}
	a, f := degradedTestApp(t, chat)
	name := appstate.WAPatchRegularLow
	f.fetchEvents = func(_ context.Context, _ string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			return nil, appstate.ErrMismatchingLTHash
		}
		return nil, nil
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		return types.MessageID("bg-req"), nil
	}
	f.hasLocalAppState = true
	forceBrokenFullSync(t, a, f, name)

	if err := a.ArchiveChat(t.Context(), chat, true); err != nil {
		t.Fatalf("ArchiveChat on flagged collection: %v", err)
	}
	if stored, err := a.db.GetChat(chat.String()); err != nil || !stored.Archived {
		t.Fatalf("archive not persisted: %+v, %v", stored, err)
	}
	required, err := a.db.AppStateRecoveryRequired(string(name))
	if err != nil {
		t.Fatal(err)
	}
	if required {
		t.Fatal("flagged collection kept a replay marker after a fully persisted write")
	}
}

// (b) A flagged collection with replay markers and local state writes without
// waiting for the phone, and sends at most one background recovery request
// per window.
func TestDegradedReplayWritesWithoutWaitingForPrimary(t *testing.T) {
	chat := types.JID{User: "15550000021", Server: types.DefaultUserServer}
	a, f := degradedTestApp(t, chat)
	name := appstate.WAPatchRegularLow
	var recoveryRequests, fullSyncs atomic.Int32
	f.fetchEvents = func(_ context.Context, _ string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			fullSyncs.Add(1)
			return nil, appstate.ErrMismatchingLTHash
		}
		return nil, nil
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		recoveryRequests.Add(1)
		return types.MessageID("bg-req"), nil
	}
	f.hasLocalAppState = true
	forceBrokenFullSync(t, a, f, name)
	// Stale replay debt from an earlier write.
	if _, err := a.db.MarkAppStateRecoveryGeneration(string(name)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := a.ArchiveChat(ctx, chat, true); err != nil {
		t.Fatalf("ArchiveChat with replay debt on flagged collection: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("degraded write took %v; it must not wait for the phone", elapsed)
	}
	if stored, err := a.db.GetChat(chat.String()); err != nil || !stored.Archived {
		t.Fatalf("archive not persisted: %+v, %v", stored, err)
	}
	// forceBrokenFullSync swaps in its own hook, so the counter only sees
	// syncs issued by the writes themselves.
	if got := fullSyncs.Load(); got != 0 {
		t.Fatalf("full syncs during the degraded write = %d, want 0 (no full replay)", got)
	}
	if got := recoveryRequests.Load(); got != 1 {
		t.Fatalf("background recovery requests = %d, want 1", got)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || required {
		t.Fatalf("replay debt after degraded write = %v, %v; want cleared", required, err)
	}

	// A second degraded write inside the rate-limit window sends no new request.
	if _, err := a.db.MarkAppStateRecoveryGeneration(string(name)); err != nil {
		t.Fatal(err)
	}
	if err := a.ArchiveChat(ctx, chat, false); err != nil {
		t.Fatalf("second degraded write: %v", err)
	}
	if got := recoveryRequests.Load(); got != 1 {
		t.Fatalf("background recovery requests after second write = %d, want still 1 (rate-limited)", got)
	}
}

// (c) An unflagged collection keeps today's behavior exactly: the write
// marker stays durable, and the next write replays with a full sync.
func TestUnflaggedCollectionKeepsDurableReplayDebt(t *testing.T) {
	chat := types.JID{User: "15550000022", Server: types.DefaultUserServer}
	a, f := degradedTestApp(t, chat)
	name := appstate.WAPatchRegularLow
	var recoveryRequests, fullSyncs atomic.Int32
	f.fetchEvents = func(_ context.Context, _ string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			fullSyncs.Add(1)
		}
		return nil, nil
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		recoveryRequests.Add(1)
		return types.MessageID("req"), nil
	}
	f.hasLocalAppState = true

	if err := a.ArchiveChat(t.Context(), chat, true); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || !required {
		t.Fatalf("marker after first write = %v, %v; want kept (durable replay debt)", required, err)
	}
	if err := a.ArchiveChat(t.Context(), chat, false); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if got := fullSyncs.Load(); got != 1 {
		t.Fatalf("full replays on second write = %d, want 1", got)
	}
	if got := recoveryRequests.Load(); got != 0 {
		t.Fatalf("recovery requests on healthy collection = %d, want 0", got)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || !required {
		t.Fatalf("marker after second write = %v, %v; want kept", required, err)
	}
}

// (d) A flagged collection WITHOUT local state must not take the degraded
// path: it still waits for the primary's recovery answer.
func TestDegradedFlagWithoutLocalStateStillWaitsForPrimary(t *testing.T) {
	chat := types.JID{User: "15550000023", Server: types.DefaultUserServer}
	a, f := degradedTestApp(t, chat)
	name := appstate.WAPatchRegularLow
	var recoveryRequests atomic.Int32
	f.fetchEvents = func(_ context.Context, _ string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			return nil, appstate.ErrMismatchingLTHash
		}
		return nil, nil
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		recoveryRequests.Add(1)
		return types.MessageID("req"), nil
	}
	// No local state: f.hasLocalAppState stays false.
	forceBrokenFullSync(t, a, f, name)
	if _, err := a.db.MarkAppStateRecoveryGeneration(string(name)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	err := a.ArchiveChat(ctx, chat, true)
	if err == nil {
		t.Fatal("write without local state must keep waiting for the primary and fail on timeout")
	}
	if got := recoveryRequests.Load(); got != 1 {
		t.Fatalf("synchronous recovery requests = %d, want 1", got)
	}
	if required, checkErr := a.db.AppStateRecoveryRequired(string(name)); checkErr != nil || !required {
		t.Fatalf("marker after failed wait = %v, %v; want kept", required, checkErr)
	}
}

// (e) Right after a restart the flag is not armed yet. The first write with
// replay debt pays one full sync (which arms the flag) and must then take the
// degraded path in the same call instead of waiting minutes for the phone.
func TestFirstReplayAfterRestartArmsFlagAndSkipsPhoneWait(t *testing.T) {
	chat := types.JID{User: "15550000024", Server: types.DefaultUserServer}
	a, f := degradedTestApp(t, chat)
	name := appstate.WAPatchRegularLow
	var recoveryRequests, fullSyncs atomic.Int32
	f.fetchEvents = func(_ context.Context, _ string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			fullSyncs.Add(1)
			return nil, appstate.ErrMismatchingLTHash
		}
		return nil, nil
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		recoveryRequests.Add(1)
		return types.MessageID("bg-req"), nil
	}
	f.hasLocalAppState = true
	if a.isAppStateFullSyncBroken(name) {
		t.Fatal("flag must start unarmed in a fresh process")
	}
	// Replay debt left by a write before the restart.
	if _, err := a.db.MarkAppStateRecoveryGeneration(string(name)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := a.ArchiveChat(ctx, chat, true); err != nil {
		t.Fatalf("first write after restart: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("first write after restart took %v; it must not wait for the phone", elapsed)
	}
	if got := fullSyncs.Load(); got != 1 {
		t.Fatalf("full syncs = %d, want exactly 1 (the one that arms the flag)", got)
	}
	if !a.isAppStateFullSyncBroken(name) {
		t.Fatal("the failed full sync must arm the flag")
	}
	if got := recoveryRequests.Load(); got != 1 {
		t.Fatalf("recovery requests = %d, want 1 background request", got)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || required {
		t.Fatalf("replay debt after the write = %v, %v; want cleared", required, err)
	}
}
