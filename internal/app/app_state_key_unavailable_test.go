package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

var missingRegularHighKey = fmt.Errorf("failed to decode app state regular_high patches: failed to decode snapshot of v80: failed to get key 000000007C57 to decode mutation: %w", appstate.ErrKeyNotFound)

func completeRecoveryOnRequest(f *fakeWA) {
	f.onAppStateRecovery = func(name string) {
		go f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchName(name), Version: 81, Recovery: true})
	}
}

func recoveredCollections(f *fakeWA) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.appStateRecoveries...)
}

func TestAppStateKeyUnavailableRecoversCollectionAlreadyMissingKey(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	completeRecoveryOnRequest(f)
	// Delta sync: regular_high misses the key, the others are fine. Recovery:
	// the full sync still misses it, so only a snapshot can help.
	f.appStateFetchErrs = []error{missingRegularHighKey, nil, nil, missingRegularHighKey}

	var recoveries sync.Map
	a.syncAppStateDeltas(t.Context(), &recoveries)
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("recovery requested before the primary answered: %v", got)
	}

	a.handleAppStateKeyUnavailable(t.Context(), &wa.AppStateKeyUnavailable{KeyID: []byte{0, 0, 0, 0, 0x7C, 0x57}}, &recoveries)
	a.appStateRecoveryWorkers.Wait()

	if got := recoveredCollections(f); !slices.Equal(got, []string{string(appstate.WAPatchRegularHigh)}) {
		t.Fatalf("recovered collections = %v, want [regular_high]", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if last := f.appStateFetches[len(f.appStateFetches)-1]; last.name != string(appstate.WAPatchRegularHigh) || !last.fullSync {
		t.Fatalf("recovery did not try a full sync first: %+v", last)
	}
}

func TestAppStateKeyMissingAfterPrimaryAnsweredEmptyRecoversImmediately(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	completeRecoveryOnRequest(f)
	f.appStateFetchErrs = []error{missingRegularHighKey, nil, nil, missingRegularHighKey}

	var recoveries sync.Map
	// whatsmeow's own fetch on connect can trigger the key request, so the empty
	// share may arrive before wacli has seen any collection fail.
	a.handleAppStateKeyUnavailable(t.Context(), &wa.AppStateKeyUnavailable{KeyID: []byte{0x7C, 0x57}}, &recoveries)
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("recovery requested with no collection missing a key: %v", got)
	}
	a.syncAppStateDeltas(t.Context(), &recoveries)
	a.appStateRecoveryWorkers.Wait()

	if got := recoveredCollections(f); !slices.Equal(got, []string{string(appstate.WAPatchRegularHigh)}) {
		t.Fatalf("recovered collections = %v, want [regular_high]", got)
	}
}

func TestChatStateWriteSkipsKeyWaitWhenPrimaryLacksKey(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	completeRecoveryOnRequest(f)
	missingLow := fmt.Errorf("failed to decode regular_low patch: %w", appstate.ErrKeyNotFound)
	f.appStateFetchErr = missingLow

	var recoveries sync.Map
	a.handleAppStateKeyUnavailable(t.Context(), &wa.AppStateKeyUnavailable{KeyID: []byte{0x7C, 0x57}}, &recoveries)

	// Without the primary's answer this write waits up to five minutes for a
	// key delivery that never comes.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := a.ArchiveChat(ctx, types.JID{User: "456", Server: types.DefaultUserServer}, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ArchiveChat waited %s for a key the primary does not have", elapsed)
	}
	if got := recoveredCollections(f); !slices.Equal(got, []string{string(appstate.WAPatchRegularLow)}) {
		t.Fatalf("recovered collections = %v, want [regular_low]", got)
	}
}
