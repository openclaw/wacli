package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// TestSyncSendsAvailablePresenceOnConnected ensures that when a sync session
// connects, wacli broadcasts types.PresenceAvailable, and that it sends
// types.PresenceUnavailable again when the session ends so the phone still
// receives push notifications.
func TestSyncSendsAvailablePresenceOnConnected(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	raw := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := a.Sync(ctx, SyncOptions{
			Mode:         SyncModeOnce,
			AllowQR:      false,
			IdleExit:     time.Millisecond,
			WarnNoLimits: false,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	if !strings.Contains(raw, "\nConnected.\n") {
		t.Fatalf("missing connected line in stderr:\n%s", raw)
	}

	assertPresenceCalls(t, f, types.PresenceAvailable, types.PresenceAvailable, types.PresenceUnavailable)
}

// TestSyncSendsAvailablePresenceOnPushNameSetting ensures that once the
// server tells us our pushname, wacli sends another presence update. This
// mirrors the behavior of go-whatsapp-web-multidevice and is important because
// whatsmeow's SendPresence requires a pushname to be set. When the sync
// session ends, PresenceUnavailable is sent as well.
func TestSyncSendsAvailablePresenceOnPushNameSetting(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	// Fake the server sending a pushname update after the initial connect.
	f.connectEvents = []interface{}{&events.PushNameSetting{}}

	raw := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := a.Sync(ctx, SyncOptions{
			Mode:         SyncModeOnce,
			AllowQR:      false,
			IdleExit:     time.Millisecond,
			WarnNoLimits: false,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	if !strings.Contains(raw, "\nConnected.\n") {
		t.Fatalf("missing connected line in stderr:\n%s", raw)
	}

	assertPresenceCalls(t, f, types.PresenceAvailable, types.PresenceAvailable, types.PresenceAvailable, types.PresenceUnavailable)
}

// TestSyncQuietPresenceModeSkipsAvailablePresence verifies the personal-mirror
// mode does not mark the linked device globally available while sync is live,
// including the lower-level authenticated-connect path, but still sends
// unavailable on cleanup as a safe final state.
func TestSyncQuietPresenceModeSkipsAvailablePresence(t *testing.T) {
	tests := []struct {
		name          string
		connectEvents []interface{}
	}{
		{name: "connected only"},
		{name: "push name", connectEvents: []interface{}{&events.PushNameSetting{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			f.connectEvents = tt.connectEvents
			a.wa = f

			captureStderr(t, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_, err := a.Sync(ctx, SyncOptions{
					Mode:         SyncModeOnce,
					AllowQR:      false,
					IdleExit:     time.Millisecond,
					WarnNoLimits: false,
					PresenceMode: SyncPresenceModeQuiet,
				})
				if err != nil {
					t.Fatalf("Sync: %v", err)
				}
			})

			assertPresenceCalls(t, f, types.PresenceUnavailable)
		})
	}
}

func TestSyncQuietPresenceModeSkipsAvailablePresenceAfterReconnect(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reconnected := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.mu.Lock()
				connectCalls := f.connectCalls
				f.mu.Unlock()
				if connectCalls >= 2 {
					close(reconnected)
					cancel()
					return
				}
			}
		}
	}()

	captureStderr(t, func() {
		_, err := a.Sync(ctx, SyncOptions{
			Mode:         SyncModeFollow,
			AllowQR:      false,
			MaxReconnect: time.Second,
			PresenceMode: SyncPresenceModeQuiet,
			AfterConnect: func(context.Context) error {
				f.emit(&events.StreamReplaced{})
				return nil
			},
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Sync: %v", err)
		}
	})

	select {
	case <-reconnected:
	default:
		t.Fatal("expected StreamReplaced to trigger reconnect")
	}
	assertPresenceCalls(t, f, types.PresenceUnavailable)
}

// TestSyncPresenceFailureWarnsAndContinues verifies that a failed presence
// update is logged as a warning and does not abort the sync loop.
func TestSyncPresenceFailureWarnsAndContinues(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.sendPresenceErr = errors.New("presence offline")
	a.wa = f

	raw := captureStderr(t, func() {
		// Enable NDJSON events so warnings surface on stderr as JSON events.
		a.opts.Events = out.NewEventWriter(os.Stderr, true)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := a.Sync(ctx, SyncOptions{
			Mode:         SyncModeOnce,
			AllowQR:      false,
			IdleExit:     time.Millisecond,
			WarnNoLimits: false,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	waitForPresenceCalls(t, f, 3)

	if !strings.Contains(raw, "send_presence_failed") {
		t.Fatalf("missing send_presence_failed warning event in stderr:\n%s", raw)
	}
	if !strings.Contains(raw, "presence offline") {
		t.Fatalf("missing original error text in warning event in stderr:\n%s", raw)
	}
}

// TestSyncSendsAvailablePresenceOnConnectedIsSynchronous ensures the event
// handler presence call happens within the sync lifetime without requiring a
// follow-mode loop.
func TestSyncSendsAvailablePresenceOnConnectedIsSynchronous(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := a.Sync(ctx, SyncOptions{
			Mode:         SyncModeOnce,
			AllowQR:      false,
			IdleExit:     time.Millisecond,
			WarnNoLimits: false,
		})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	})

	waitForPresenceCalls(t, f, 3)
}

func assertPresenceCalls(t *testing.T, f *fakeWA, want ...types.Presence) {
	t.Helper()
	waitForPresenceCalls(t, f, len(want))

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.presenceCalls) != len(want) {
		t.Fatalf("got %d presence calls, want %d", len(f.presenceCalls), len(want))
	}
	for i, got := range f.presenceCalls {
		if got != want[i] {
			t.Fatalf("presence call %d = %v, want %v", i, got, want[i])
		}
	}
}

func waitForPresenceCalls(t *testing.T, f *fakeWA, want int) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.presenceCalls)
		f.mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	n := len(f.presenceCalls)
	f.mu.Unlock()
	if n != want {
		t.Fatalf("timed out waiting for presence calls: got %d, want %d", n, want)
	}
}

// TestSyncSendsUnavailableOnErrorExit ensures the unavailable presence is
// sent even when Sync returns early due to an AfterConnect error, not just
// on the success path. This covers the P1 finding where storage-limit or
// reconnect/error exits could leave the linked device marked available.
func TestSyncSendsUnavailableOnErrorExit(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := a.Sync(ctx, SyncOptions{
			Mode:         SyncModeOnce,
			AllowQR:      false,
			IdleExit:     time.Millisecond,
			WarnNoLimits: false,
			AfterConnect: func(context.Context) error {
				return errors.New("simulated after-connect failure")
			},
		})
		if err == nil {
			t.Fatalf("expected error from AfterConnect")
		}
	})

	// The initial authenticated-connect presence and Connected event both send
	// available in normal mode. The defer then sends unavailable on the error
	// exit, proving cleanup runs on all post-connect exits.
	assertPresenceCalls(t, f, types.PresenceAvailable, types.PresenceAvailable, types.PresenceUnavailable)
}
