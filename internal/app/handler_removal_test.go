package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"bytes"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Every handler removal a command that connected by itself can reach must not
// wait for the event the one-shot store is handling: whatsmeow's
// RemoveEventHandler waits for the event being dispatched (dispatchLockingWA
// models that), and the store may be inside a history download or a write
// waiting on a busy database. One test per removal path.

// busyOneShotStore connects through App.Connect and leaves the one-shot store
// inside a history download that does not return until the returned func is
// called (also at cleanup).
func busyOneShotStore(t *testing.T, a *App, w *dispatchLockingWA, ctx context.Context) func() {
	t.Helper()
	if err := a.Connect(ctx, false, nil); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	w.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		close(entered)
		<-release
		return historySyncWithTextMessages(oneShotChat(), at, "h-busy").Data, nil
	}
	own := types.NewJID("15550000000", types.DefaultUserServer)
	syncType := waE2E.HistorySyncType_RECENT
	go w.emit(&events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: own, Sender: own, IsFromMe: true}, ID: "hist-busy", Timestamp: at},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			HistorySyncNotification: &waE2E.HistorySyncNotification{SyncType: &syncType}}},
	})
	<-entered
	return unblock
}

// returnsWithin fails if fn has not returned within limit; it then releases
// the store so the test ends.
func returnsWithin(t *testing.T, what string, limit time.Duration, unblock func(), fn func()) {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() { fn(); close(done) }()
	select {
	case <-done:
		t.Logf("%s returned after %s while the one-shot store was still busy", what, time.Since(start).Round(time.Millisecond))
	case <-time.After(limit):
		unblock()
		<-done
		t.Fatalf("%s waited for the event the one-shot store was handling (returned only after the store was released, %s)", what, time.Since(start).Round(time.Millisecond))
	}
}

func lockingTestApp(t *testing.T) (*App, *dispatchLockingWA, *bytes.Buffer) {
	t.Helper()
	a := newTestApp(t)
	w := &dispatchLockingWA{fakeWA: newFakeWA()}
	a.wa = w
	var evbuf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&evbuf, true)
	return a, w, &evbuf
}

// Chat-state commands and contacts save remove their app-state persistence
// handler in a deferred cleanup, before App.Close. Once removed it is not
// called again.
func TestPersistenceHandlerRemovalDoesNotWaitForTheOneShotStore(t *testing.T) {
	a, w, evbuf := lockingTestApp(t)
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	unblock := busyOneShotStore(t, a, w, t.Context())
	returnsWithin(t, "removing the app-state persistence handler", 2*time.Second, unblock, remove)
	// The store now handles app state itself (no persister left): one warning,
	// so the removed persister was not called.
	w.emit(&wa.AppStateKeyUnavailable{KeyID: []byte{0xAB}})
	if n := strings.Count(evbuf.String(), `"code":"app_state_key_unavailable"`); n != 1 {
		t.Fatalf("app-state warnings after the persister was removed = %d, want 1:\n%s", n, evbuf.String())
	}
}

// A chat-state write that asks the phone for an app-state recovery stops its
// capture during the command.
func TestRecoveryCaptureStopDoesNotWaitForTheOneShotStore(t *testing.T) {
	a, w, _ := lockingTestApp(t)
	unblock := busyOneShotStore(t, a, w, t.Context())
	capture := a.newAppStateRecoveryCapture(appstate.WAPatchRegularLow)
	returnsWithin(t, "stopping an app-state recovery capture", 2*time.Second, unblock, capture.stop)
}

// A late app-state recovery keeps a worker waiting; Close stops it and waits
// for it, and the worker stops its capture.
func TestLateRecoveryWorkerDoesNotHoldCloseForTheOneShotStore(t *testing.T) {
	a, w, evbuf := lockingTestApp(t)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	unblock := busyOneShotStore(t, a, w, ctx)
	a.adoptLateAppStateRecovery(appstate.WAPatchRegularLow, 1, a.newAppStateRecoveryCapture(appstate.WAPatchRegularLow))
	took := closeWithin(t, a, 10*time.Second, unblock)
	if took > 3*time.Second {
		t.Fatalf("Close took %s with a late recovery pending; the command's deadline was 300ms after connecting", took)
	}
	if !strings.Contains(evbuf.String(), `"code":"oneshot_store_incomplete"`) {
		t.Fatalf("no oneshot_store_incomplete warning for the event cut short:\n%s", evbuf.String())
	}
}

// `presence subscribe --wait` removes its answer handler when it returns.
func TestPresenceWatchDoesNotWaitForTheOneShotStore(t *testing.T) {
	a, w, _ := lockingTestApp(t)
	unblock := busyOneShotStore(t, a, w, t.Context())
	returnsWithin(t, "watching presence with a wait", 2*time.Second, unblock, func() {
		if _, err := a.WatchPresence(t.Context(), types.NewJID("15551234567", types.DefaultUserServer), 50*time.Millisecond); err != nil {
			t.Errorf("WatchPresence: %v", err)
		}
	})
}

// A media retry removes its notification handler when it returns.
func TestMediaRetryDoesNotWaitForTheOneShotStore(t *testing.T) {
	a, f, chat := exactRetryFixture(t)
	w := &dispatchLockingWA{fakeWA: f}
	a.wa = w
	unblock := busyOneShotStore(t, a, w, t.Context())
	returnsWithin(t, "retrying a media download", 2*time.Second, unblock, func() {
		if out, err := a.RetryMediaMessage(t.Context(), chat, "target", 10*time.Millisecond); err != nil || out.Status != "no_response" {
			t.Errorf("RetryMediaMessage = %+v, %v; want no_response", out, err)
		}
	})
}

// Without a one-shot store (a running sync, the commands it runs for others, a
// command that never connects) a removal is what it always was: it waits for
// a call of the handler in progress, so the caller knows the handler has
// finished.
func TestRemovalWithoutAOneShotStoreStillWaitsForACallInProgress(t *testing.T) {
	a, w, _ := lockingTestApp(t)
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	id := a.addEventHandler(func(any) {
		close(entered)
		<-release
	})
	go w.emit(&events.Connected{})
	<-entered
	removed := make(chan struct{})
	go func() { a.removeEventHandler(w, id); close(removed) }()
	select {
	case <-removed:
		close(release)
		t.Fatal("the removal returned while the handler was still running: without a one-shot store it must wait, as before")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-removed:
	case <-time.After(2 * time.Second):
		t.Fatal("the removal did not return once the handler finished")
	}
}
