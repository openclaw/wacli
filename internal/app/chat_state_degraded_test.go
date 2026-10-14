package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
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

// recoveryIntentExists reports whether one exact durable recovery intent is
// still present in wacli.db.
func recoveryIntentExists(t *testing.T, a *App, name string, id int64) bool {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var exists bool
	if err := raw.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM app_state_recovery_intents WHERE collection = ? AND id = ?)`, name, id,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// (a) Even on a flagged collection a successful write keeps its own durable
// replay intent: pinned whatsmeow's SendAppState dispatches the post-send
// fetched mutations in a goroutine after it returns, so a nil persistence
// error does not prove that batch reached wacli.db. The intent survives until
// a verified replay covers it — and later writes still proceed without the
// phone, through the degraded pre-write path.
func TestDegradedCollectionWriteKeepsDurableSendIntent(t *testing.T) {
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
	forceBrokenFullSync(t, a, f, name)
	// The primary's recovery snapshot later restored local state (the failed
	// full sync itself deleted the saved version, as stock whatsmeow does).
	f.hasLocalAppState = true

	if err := a.ArchiveChat(t.Context(), chat, true); err != nil {
		t.Fatalf("ArchiveChat on flagged collection: %v", err)
	}
	if stored, err := a.db.GetChat(chat.String()); err != nil || !stored.Archived {
		t.Fatalf("archive not persisted: %+v, %v", stored, err)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || !required {
		t.Fatalf("send intent after the write = %v, %v; want kept (post-send mutations dispatch asynchronously)", required, err)
	}

	// The kept debt must not push later writes back into the full replay plus
	// phone wait: the degraded path settles them with an incremental fetch.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := a.ArchiveChat(ctx, chat, false); err != nil {
		t.Fatalf("second write with kept send intent: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("second write took %v; it must not wait for the phone", elapsed)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || !required {
		t.Fatalf("send intent after the second write = %v, %v; want kept", required, err)
	}
}

// A live mutation advanced whatsmeow's collection cursor but its wacli.db
// persistence failed, leaving a durable recovery intent. The next degraded
// pre-write replay sees an empty incremental delta — the cursor is already
// past the mutation — which is not a replay: the intent must survive while
// the write still proceeds.
func TestDegradedReplayKeepsIntentOfUnpersistedLiveMutation(t *testing.T) {
	chat := types.JID{User: "15550000025", Server: types.DefaultUserServer}
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
	forceBrokenFullSync(t, a, f, name)
	f.hasLocalAppState = true

	// The write-ahead marker a live event leaves when its ordered persistence
	// into wacli.db fails after whatsmeow already consumed the mutation.
	markers, err := a.markLiveAppStateRecovery(&events.Archive{
		JID:    chat,
		Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
	})
	if err != nil || len(markers) != 1 {
		t.Fatalf("mark live recovery: %v, %v", markers, err)
	}
	failedGen := markers[0].generation

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := a.ArchiveChat(ctx, chat, true); err != nil {
		t.Fatalf("degraded write with unpersisted live mutation debt: %v", err)
	}
	if !recoveryIntentExists(t, a, string(name), failedGen) {
		t.Fatal("degraded replay cleared the intent of a live mutation that never reached wacli.db; an empty incremental delta is not a replay")
	}
	if required, err := a.db.AppStateRecoveryRequired(string(name)); err != nil || !required {
		t.Fatalf("replay debt after degraded write = %v, %v; want kept", required, err)
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
	forceBrokenFullSync(t, a, f, name)
	// The primary's recovery snapshot later restored local state.
	f.hasLocalAppState = true
	// Stale replay debt from an earlier write whose post-send batch was never
	// proven persisted.
	staleGen, err := a.db.MarkAppStateRecoveryGeneration(string(name))
	if err != nil {
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
	// The write proceeded, but the stale debt is not provably replayed by an
	// incremental fetch: it must survive for a later verified replay.
	if !recoveryIntentExists(t, a, string(name), staleGen) {
		t.Fatal("degraded write cleared stale replay debt it did not replay")
	}

	// A second degraded write inside the rate-limit window sends no new request.
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
	// No local state: the failed full sync deleted the saved collection
	// version (stock whatsmeow) and no recovery snapshot restored it.
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

// (e) The "full sync does not verify" flag is durable. With stock whatsmeow a
// full sync deletes the saved collection version before decoding, so right
// after the failure there is no local state and the degraded path cannot
// engage in the same process until a primary recovery snapshot restores it.
// What must work is the next process: the persisted flag makes the pre-write
// degraded path run before any destructive full sync, so a restart with
// restored local state writes without repeating the known-broken full replay
// and without waiting for the phone.
func TestFullSyncBrokenFlagPersistsAcrossRestart(t *testing.T) {
	chat := types.JID{User: "15550000024", Server: types.DefaultUserServer}
	a, f := degradedTestApp(t, chat)
	name := appstate.WAPatchRegularLow
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		return types.MessageID("bg-req"), nil
	}
	f.hasLocalAppState = true
	forceBrokenFullSync(t, a, f, name)
	// The fake models the stock SDK: the failed full sync deleted the version.
	if hasLocal, err := f.HasLocalAppState(t.Context(), string(name)); err != nil || hasLocal {
		t.Fatalf("local app state after failed full sync = %v, %v; the fake must model the destructive full sync", hasLocal, err)
	}
	// Replay debt left by a write before the restart.
	if _, err := a.db.MarkAppStateRecoveryGeneration(string(name)); err != nil {
		t.Fatal(err)
	}
	a.Close() // restart

	a2, err := New(Options{StoreDir: a.StoreDir()})
	if err != nil {
		t.Fatalf("reopen app after restart: %v", err)
	}
	t.Cleanup(a2.Close)
	f2 := &appStateContextWA{fakeWA: newFakeWA()}
	var fullSyncs, recoveryRequests atomic.Int32
	f2.fetchEvents = func(_ context.Context, _ string, fullSync, _ bool) ([]any, error) {
		if fullSync {
			fullSyncs.Add(1)
			return nil, appstate.ErrMismatchingLTHash
		}
		return nil, nil
	}
	f2.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		recoveryRequests.Add(1)
		return types.MessageID("bg-req"), nil
	}
	// The primary's recovery snapshot restored local state before the restart.
	f2.hasLocalAppState = true
	a2.wa = f2

	if !a2.isAppStateFullSyncBroken(name) {
		t.Fatal("the persisted flag must be rehydrated from wacli.db after a restart")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := a2.ArchiveChat(ctx, chat, true); err != nil {
		t.Fatalf("first write after restart: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("first write after restart took %v; it must not wait for the phone", elapsed)
	}
	if got := fullSyncs.Load(); got != 0 {
		t.Fatalf("full syncs after restart = %d, want 0 (no destructive replay on a known-broken collection)", got)
	}
	if got := recoveryRequests.Load(); got != 1 {
		t.Fatalf("recovery requests = %d, want 1 background request", got)
	}
}
