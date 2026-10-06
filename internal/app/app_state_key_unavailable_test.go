package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

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

func TestKnownEmptyKeyRecoversOneShotWithoutGlobalSignal(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	completeRecoveryOnRequest(f)
	f.appStateFetchErr = wa.ErrEmptyAppStateKeyShare
	jid := types.NewJID("15550000001", types.DefaultUserServer)
	if err := a.db.UpsertChat(jid.String(), "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := a.ArchiveChat(ctx, jid, true); err != nil {
		t.Fatal(err)
	}
	chat, err := a.db.GetChat(jid.String())
	if err != nil || !chat.Archived {
		t.Fatalf("chat=%+v err=%v", chat, err)
	}
	if got := recoveredCollections(f); !slices.Equal(got, []string{"regular_low"}) {
		t.Fatalf("recoveries=%v", got)
	}
}

func TestEmptyShareDoesNotRecoverAnUnrelatedMissingKey(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	completeRecoveryOnRequest(f)
	var stored, last atomic.Int64
	id, _ := a.addSyncEventHandler(t.Context(), SyncOptions{}, &stored, &last, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1), func(string, string) {}, nil, nil, &syncPresence{}, nil)
	defer f.RemoveEventHandler(id)
	f.emit(&wa.AppStateKeyUnavailable{KeyID: []byte{1, 2, 3}})
	f.appStateFetchErr = fmt.Errorf("different key: %w", appstate.ErrKeyNotFound)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := a.ArchiveChat(ctx, types.NewJID("15550000001", types.DefaultUserServer), true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v,want ordinary key wait", err)
	}
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("unrelated key triggered snapshot: %v", got)
	}
}

func TestKnownEmptyKeySyncErrorRecoversOnlyAffectedCollection(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	completeRecoveryOnRequest(f)
	f.appStateFetchErr = wa.ErrEmptyAppStateKeyShare
	var recoveries sync.Map
	event := &events.AppStateSyncError{Name: appstate.WAPatchRegularHigh, Error: fmt.Errorf("decode: %w", wa.ErrEmptyAppStateKeyShare)}
	a.handleAppStateSyncError(t.Context(), event, &recoveries)
	a.appStateRecoveryWorkers.Wait()
	a.handleAppStateSyncError(t.Context(), event, &recoveries)
	a.handleAppStateSyncError(t.Context(), &events.AppStateSyncError{Name: appstate.WAPatchRegularLow, Error: appstate.ErrKeyNotFound}, &recoveries)
	a.appStateRecoveryWorkers.Wait()
	if got := recoveredCollections(f); !slices.Equal(got, []string{"regular_high"}) {
		t.Fatalf("recoveries=%v", got)
	}
}

func TestKnownEmptyKeyFailedRecoveryPreventsWrite(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			f.appStateFetchErr = wa.ErrEmptyAppStateKeyShare
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				f.onAppStateRecovery = func(string) { cancel() }
			} else {
				f.appStateRecoveryErr = errors.New("snapshot unavailable")
			}
			jid := types.NewJID("15550000001", types.DefaultUserServer)
			if err := a.db.UpsertChat(jid.String(), "dm", "Synthetic", time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := a.ArchiveChat(ctx, jid, true); err == nil {
				t.Fatal("failed recovery allowed write")
			}
			required, err := a.db.AppStateRecoveryRequired("regular_low")
			if err != nil || !required {
				t.Fatalf("recovery debt lost: required=%v error=%v", required, err)
			}
			chat, err := a.db.GetChat(jid.String())
			if err != nil || chat.Archived {
				t.Fatalf("chat=%+v err=%v", chat, err)
			}
		})
	}
}
