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
// connects, wacli broadcasts types.PresenceAvailable. WhatsApp uses this node to
// update the linked device's "last active" status; without it the connection is
// alive but the app still shows a stale timestamp.
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

	waitForPresenceCalls(t, f, 1)

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.presenceCalls) != 1 {
		t.Fatalf("got %d presence calls, want 1", len(f.presenceCalls))
	}
	if f.presenceCalls[0] != types.PresenceAvailable {
		t.Fatalf("presence call = %v, want Available", f.presenceCalls[0])
	}
}

// TestSyncSendsAvailablePresenceOnPushNameSetting ensures that once the
// server tells us our pushname, wacli sends another presence update. This
// mirrors the behavior of go-whatsapp-web-multidevice and is important because
// whatsmeow's SendPresence requires a pushname to be set.
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

	waitForPresenceCalls(t, f, 2)

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.presenceCalls) != 2 {
		t.Fatalf("got %d presence calls, want 2", len(f.presenceCalls))
	}
	for i, p := range f.presenceCalls {
		if p != types.PresenceAvailable {
			t.Fatalf("presence call %d = %v, want Available", i, p)
		}
	}
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

	waitForPresenceCalls(t, f, 1)

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

	waitForPresenceCalls(t, f, 1)
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
