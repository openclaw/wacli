package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Only the server response is synthetic: decoding, verification, cursor writes
// and contact application use the pinned SDK and a disposable session sqlstore.
type sdkContactReplayWA struct {
	*sdkSessionWA
	fetch func(context.Context, bool) ([]any, error)
}

func (w *sdkContactReplayWA) FetchAppStateEvents(ctx context.Context, name string, full, only bool) ([]any, error) {
	if name != string(contactCollection) {
		return w.fakeWA.FetchAppStateEvents(ctx, name, full, only)
	}
	return w.fetch(ctx, full)
}

func contactSnapshot(contacts ...*waServerSync.SyncdPatch) *waServerSync.SyncdSnapshot {
	// The last patch's snapshot MAC covers all preceding records in this chain.
	last := contacts[len(contacts)-1]
	snapshot := &waServerSync.SyncdSnapshot{Version: last.Version, KeyID: last.KeyID, Mac: last.SnapshotMAC}
	for _, patch := range contacts {
		for _, mutation := range patch.Mutations {
			snapshot.Records = append(snapshot.Records, mutation.Record)
		}
	}
	return snapshot
}

func installContactReplay(t *testing.T, a *App, f *fakeWA) (*sdkContactReplayWA, func(context.Context, bool) ([]any, error)) {
	t.Helper()
	key := []byte{1, 2, 3}
	device, sdk, proc := newSDKSession(t, f, key, 80)
	sdk.EmitAppStateEventsOnFullSync = true
	f.hasLocalAppState = true
	f.appStateRecoveryErr = errors.New("unexpected phone recovery: server replay should suffice")
	other := types.NewJID("15550002222", types.DefaultUserServer)
	info, err := wa.BuildSaveContact(testContactPN, testContactLID, "Grace Hopper", "Grace", false)
	if err != nil {
		t.Fatal(err)
	}
	clear, err := wa.BuildSaveContact(other, testContactLID, "placeholder", "", false)
	if err != nil {
		t.Fatal(err)
	}
	clear.Mutations[0].Value.ContactAction.FullName = nil
	clear.Mutations[0].Value.ContactAction.FirstName = nil
	info.Mutations = append(info.Mutations, clear.Mutations...)
	raw, err := proc.EncodePatch(t.Context(), key, appstate.HashState{Version: 80}, info)
	if err != nil {
		t.Fatal(err)
	}
	patch := &waServerSync.SyncdPatch{}
	if err := proto.Unmarshal(raw, patch); err != nil {
		t.Fatal(err)
	}
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(81)}
	snapshot := contactSnapshot(patch)
	w := &sdkContactReplayWA{sdkSessionWA: &sdkSessionWA{fakeWA: f, device: device}}
	a.wa = w
	replay := func(ctx context.Context, full bool) ([]any, error) {
		if !full {
			return nil, errors.New("expected full contact replay")
		}
		// Same reset as SDK fetchAppState, without a network connection.
		if err := device.AppState.DeleteAppStateVersion(ctx, string(contactCollection)); err != nil {
			return nil, err
		}
		var evts []any
		_, err := sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, appstate.HashState{}, &appstate.PatchList{Name: contactCollection, Snapshot: snapshot}, true, &evts)
		return evts, err
	}
	w.fetch = replay
	return w, replay
}

func TestContactFullReplayMirrorsSnapshotAndSettlesOlderDebt(t *testing.T) {
	for _, path := range []string{"pre-write", "startup", "live mismatch"} {
		t.Run(path, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			w, replay := installContactReplay(t, a, f)
			other := types.NewJID("15550002222", types.DefaultUserServer)
			if err := a.db.UpsertContact(other.String(), other.User, "Push", "Old Name", "Old", "Biz"); err != nil {
				t.Fatal(err)
			}
			save, err := a.beginLocalAppStateWrite(contactCollection)
			if err != nil {
				t.Fatal(err)
			}
			live, err := a.markLiveAppStateRecovery(contactEvent(other, "", ""))
			if err != nil {
				t.Fatal(err)
			}
			fullFetches := 0
			w.fetch = func(ctx context.Context, full bool) ([]any, error) {
				if full {
					fullFetches++
				}
				return replay(ctx, full)
			}
			switch path {
			case "pre-write":
				if err := a.syncChatStateBeforeWrite(t.Context(), contactCollection); err != nil {
					t.Fatal(err)
				}
			case "startup":
				a.syncAppStateDeltas(t.Context(), &sync.Map{})
			case "live mismatch":
				a.recoverAppStateCollection(t.Context(), string(contactCollection), &sync.Map{}, time.Second)
			}
			if fullFetches != 1 || contactRecoveryRequired(t, a) || recoveryIntentExists(t, a, string(contactCollection), save.generation) || recoveryIntentExists(t, a, string(contactCollection), live[0].generation) {
				t.Fatalf("verified full replay must settle older save/live debt: full fetches=%d", fullFetches)
			}
			if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
				t.Fatalf("snapshot contact not mirrored: %+v", got)
			}
			if got := storedContact(t, a, other); got.full != "" || got.first != "" || got.push != "Push" || got.business != "Biz" {
				t.Fatalf("snapshot clear was not authoritative: %+v", got)
			}
		})
	}
}

func TestContactPersistenceHardKillReplaysOnNextStart(t *testing.T) {
	const helperEnv = "WACLI_TEST_CONTACT_PERSIST_KILL"
	if dir := os.Getenv(helperEnv); dir != "" {
		a, err := New(Options{StoreDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		f := newFakeWA()
		a.wa = f
		f.applyContactBatch(contactEvent(testContactPN, "Grace Hopper", "Grace"))
		// Stop persistence after its durable live-event marker, before the mirror.
		a.contactDrainMu.Lock()
		go a.handleAppStatePersistenceEvent(t.Context(), contactEvent(testContactPN, "Grace Hopper", "Grace"), nil)
		for !contactRecoveryRequired(t, a) {
			time.Sleep(time.Millisecond)
		}
		fmt.Println("contact persistence marked")
		select {} // Parent kills us; no Close or deferred cleanup may run.
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestContactPersistenceHardKillReplaysOnNextStart$")
	cmd.Env = append(os.Environ(), helperEnv+"="+dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if line != "contact persistence marked\n" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("child did not reach persistence boundary: %q %s", line, stderr.String())
		}
	case <-ctx.Done():
		_ = cmd.Wait()
		t.Fatalf("child persistence boundary timed out: %s", stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child exited normally rather than being killed")
	}
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if !contactRecoveryRequired(t, a) || storedContact(t, a, testContactPN).found {
		t.Fatal("kill did not leave an unmirrored durable marker")
	}
	installContactReplay(t, a, newFakeWA())
	a.syncAppStateDeltas(t.Context(), &sync.Map{})
	if contactRecoveryRequired(t, a) || storedContact(t, a, testContactPN).full != "Grace Hopper" {
		t.Fatal("startup did not replay the contact lost by hard kill")
	}
}

func TestFlaggedContactRecoveryUsesMainPolicy(t *testing.T) {
	for _, path := range []string{"pre-write", "startup", "live mismatch", "missing key"} {
		t.Run(path, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			if err := a.db.SetAppStateFullSyncBroken(string(contactCollection), true); err != nil {
				t.Fatal(err)
			}
			older, err := a.db.MarkAppStateRecoveryGeneration(string(contactCollection))
			if err != nil {
				t.Fatal(err)
			}
			if path == "missing key" {
				f.appStateFetchErrs = []error{appstate.ErrKeyNotFound, nil}
			}
			switch path {
			case "startup":
				a.syncAppStateDeltas(t.Context(), &sync.Map{})
			case "live mismatch":
				a.recoverAppStateCollection(t.Context(), string(contactCollection), &sync.Map{}, time.Second)
			default:
				if err := a.syncChatStateBeforeWrite(t.Context(), contactCollection); err != nil {
					t.Fatal(err)
				}
			}
			degraded := path == "pre-write" || path == "startup"
			wantFull, wantPhone := 1, 0
			if degraded {
				wantFull, wantPhone = 0, 1
			}
			// Count the contact collection's own phone requests: a store without
			// the Keep chats archived preference also asks for regular_low.
			contactRequests := 0
			for _, name := range recoveredCollections(f) {
				if name == string(contactCollection) {
					contactRequests++
				}
			}
			if fullSyncFetches(f, contactCollection) != wantFull || contactRequests != wantPhone || recoveryIntentExists(t, a, string(contactCollection), older) != degraded {
				t.Fatalf("flagged %s did not follow main: fetches=%+v recoveries=%v debt=%v", path, f.appStateFetches, recoveredCollections(f), contactRecoveryRequired(t, a))
			}
		})
	}
}

func TestContactPreWriteMissingKeyReplaysConsumedNames(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	f.appStateRecoveryErr = errors.New("unexpected phone snapshot")
	key, laterKey := []byte{1, 2, 3}, []byte{9, 9, 9}
	device, sdk, proc := newSDKSession(t, f, key, 10)
	sdk.EmitAppStateEventsOnFullSync = true
	initial := appstate.HashState{Version: 10}
	other := types.NewJID("15550002222", types.DefaultUserServer)
	first := encodeContactPatch(t, proc, key, initial, 11, testContactPN, "Ada Lovelace", "Ada")
	_, afterFirst, err := proc.DecodePatches(t.Context(), &appstate.PatchList{Name: contactCollection, Patches: []*waServerSync.SyncdPatch{first}}, initial, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := device.AppStateKeys.PutAppStateSyncKey(t.Context(), laterKey, waStore.AppStateSyncKey{Data: bytes.Repeat([]byte{0xcd}, 32), Fingerprint: []byte{2}, Timestamp: 2}); err != nil {
		t.Fatal(err)
	}
	second := encodeContactPatch(t, proc, laterKey, afterFirst, 12, other, "Grace Hopper", "Grace")
	snapshot := contactSnapshot(first, second)
	if err := device.AppState.DeleteAppStateVersion(t.Context(), string(contactCollection)); err != nil {
		t.Fatal(err)
	}
	if err := device.AppState.PutAppStateVersion(t.Context(), string(contactCollection), 10, initial.Hash); err != nil {
		t.Fatal(err)
	}
	keys := &withheldKeyStore{AppStateSyncKeyStore: device.AppStateKeys, withheld: laterKey}
	device.AppStateKeys = keys
	w := &sdkContactReplayWA{sdkSessionWA: &sdkSessionWA{fakeWA: f, device: device}}
	a.wa = w
	calls := 0
	w.fetch = func(ctx context.Context, full bool) ([]any, error) {
		calls++
		var evts []any
		if calls == 1 {
			if full {
				t.Fatal("first fetch must be incremental")
			}
			_, err := sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, initial, &appstate.PatchList{Name: contactCollection, Patches: []*waServerSync.SyncdPatch{first, second}}, false, &evts)
			stored, storeErr := device.Contacts.GetContact(ctx, testContactPN)
			if !errors.Is(err, appstate.ErrKeyNotFound) || cursorVersion(t, device) != 11 || storeErr != nil || stored.Found || len(evts) != 0 {
				t.Fatalf("partial-decode setup failed: err=%v stored=%+v events=%d", err, stored, len(evts))
			}
			return nil, err
		}
		if !full {
			return nil, errors.New("incremental retry would silently skip the consumed name")
		}
		keys.delivered.Store(true)
		if err := device.AppState.DeleteAppStateVersion(ctx, string(contactCollection)); err != nil {
			return nil, err
		}
		_, err := sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, appstate.HashState{}, &appstate.PatchList{Name: contactCollection, Snapshot: snapshot}, true, &evts)
		return evts, err
	}
	if err := a.syncChatStateBeforeWrite(t.Context(), contactCollection); err != nil {
		t.Fatal(err)
	}
	for jid, want := range map[types.JID]string{testContactPN: "Ada Lovelace", other: "Grace Hopper"} {
		stored, err := device.Contacts.GetContact(t.Context(), jid)
		if err != nil || stored.FullName != want || storedContact(t, a, jid).full != want {
			t.Fatalf("full replay did not restore consumed name %s: SDK=%+v err=%v", jid, stored, err)
		}
	}
	if calls != 2 || contactRecoveryRequired(t, a) {
		t.Fatalf("replay did not settle debt: calls=%d debt=%v", calls, contactRecoveryRequired(t, a))
	}
}

type expirySensitiveContactResolver struct{ WAClient }

func (w expirySensitiveContactResolver) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	if ctx.Err() != nil {
		return jid
	}
	return w.WAClient.ResolveLIDToPN(ctx, jid)
}

func TestContactFetchCancellationStillMirrorsUnderCanonicalJID(t *testing.T) {
	for _, emission := range []string{"without event", "with event"} {
		t.Run(emission, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			a.wa = expirySensitiveContactResolver{f}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.appStateFetchEvent = func(string, bool, bool) any {
				f.applyContactBatch(contactEvent(testContactLID, "Grace Hopper", "Grace"))
				cancel()
				if emission == "with event" {
					return contactEvent(testContactLID, "Grace Hopper", "Grace")
				}
				return nil
			}
			_, _ = a.fetchAndPersistAppState(ctx, contactCollection, false, &appStatePersistenceTracker{})
			if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" {
				t.Fatalf("expired fetch dropped or misfiled the applied LID name: %+v", got)
			}
			if got := storedContact(t, a, testContactLID); got.found {
				t.Fatalf("expired fetch created a stale LID row: %+v", got)
			}
		})
	}
}
