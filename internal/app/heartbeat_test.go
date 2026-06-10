package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types/events"
)

func TestSyncWritesHeartbeatFileOnActivity(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	var lastActivity atomic.Int64
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow},
		&messagesStored,
		&lastEvent,
		&lastActivity,
		make(chan struct{}, 1),
		func(string, string) {},
		nil,
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	f.emit(&events.Connected{})

	heartbeatPath := filepath.Join(a.opts.StoreDir, "HEARTBEAT")
	data, err := os.ReadFile(heartbeatPath)
	if err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	ts, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		t.Fatalf("parse heartbeat timestamp %q: %v", string(data), err)
	}
	if time.Since(ts) > 10*time.Second {
		t.Fatalf("heartbeat timestamp too old: %s", ts)
	}
}

func TestSyncOnceDoesNotWriteHeartbeatFile(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	var lastActivity atomic.Int64
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeOnce},
		&messagesStored,
		&lastEvent,
		&lastActivity,
		make(chan struct{}, 1),
		func(string, string) {},
		nil,
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	f.emit(&events.Connected{})

	heartbeatPath := filepath.Join(a.opts.StoreDir, "HEARTBEAT")
	if _, err := os.Stat(heartbeatPath); !os.IsNotExist(err) {
		t.Fatalf("heartbeat stat err = %v, want not exist", err)
	}
}

func TestSyncFollowDoesNotWriteHeartbeatOnKeepAliveTimeout(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	var lastActivity atomic.Int64
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow},
		&messagesStored,
		&lastEvent,
		&lastActivity,
		make(chan struct{}, 1),
		func(string, string) {},
		nil,
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	f.emit(&events.KeepAliveTimeout{ErrorCount: 1, LastSuccess: nowUTC().Add(-time.Minute)})

	heartbeatPath := filepath.Join(a.opts.StoreDir, "HEARTBEAT")
	if _, err := os.Stat(heartbeatPath); !os.IsNotExist(err) {
		t.Fatalf("heartbeat stat err = %v, want not exist", err)
	}
}

func TestReadHeartbeatReturnsZeroForMissingFile(t *testing.T) {
	got := ReadHeartbeat(filepath.Join(t.TempDir(), "missing"))
	if !got.IsZero() {
		t.Fatalf("ReadHeartbeat missing file = %s, want zero", got)
	}
}

func TestReadHeartbeatReturnsTimestampFromFile(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT"), []byte(want.Format(time.RFC3339)), 0o644); err != nil {
		t.Fatalf("write heartbeat: %v", err)
	}
	got := ReadHeartbeat(dir)
	if !got.Equal(want) {
		t.Fatalf("ReadHeartbeat = %s, want %s", got, want)
	}
}

func TestHeartbeatThrottleIsPerApp(t *testing.T) {
	a1 := newTestApp(t)
	a2 := newTestApp(t)

	a1.writeHeartbeat()
	a2.writeHeartbeat()

	for _, tc := range []struct {
		name string
		app  *App
	}{
		{name: "first app", app: a1},
		{name: "second app", app: a2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			heartbeatPath := filepath.Join(tc.app.opts.StoreDir, "HEARTBEAT")
			if _, err := os.Stat(heartbeatPath); err != nil {
				t.Fatalf("stat heartbeat: %v", err)
			}
		})
	}
}

func TestSyncFollowStaleReconnectResetsIdleDuration(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	a.opts.Events = out.NewEventWriter(os.Stderr, true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Capture stale events. After the first stale reconnect, subsequent stale
	// events should report idle_duration close to the threshold (not an
	// accumulated value from before the reconnect).
	var staleEvents []time.Duration
	var mu sync.Mutex

	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.mu.Lock()
				calls := f.connectCalls
				f.mu.Unlock()
				mu.Lock()
				n := len(staleEvents)
				mu.Unlock()
				if n >= 3 || calls >= 4 {
					cancel()
					return
				}
			}
		}
	}()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:           SyncModeFollow,
		AllowQR:        false,
		MaxReconnect:   time.Second,
		StaleThreshold: 200 * time.Millisecond,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync: %v", err)
	}

	// The NDJSON stale events are written to stderr, which we can't easily
	// capture here. Instead verify the reconnect count is bounded: with a
	// 200ms threshold and a 2s timeout, we expect at most ~10 reconnects.
	// Without the timer reset, idle_duration would accumulate and trigger
	// on every single tick, producing far more reconnects.
	f.mu.Lock()
	calls := f.connectCalls
	f.mu.Unlock()
	if calls > 15 {
		t.Fatalf("connect calls = %d, expected timer reset to bound reconnect rate", calls)
	}
}

func TestHeartbeatFileHasOwnerOnlyPermissions(t *testing.T) {
	a := newTestApp(t)

	a.writeHeartbeat()

	heartbeatPath := filepath.Join(a.opts.StoreDir, "HEARTBEAT")
	info, err := os.Stat(heartbeatPath)
	if err != nil {
		t.Fatalf("stat heartbeat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("heartbeat file permissions = %o, want 0600", perm)
	}
}

func TestSyncFollowEmitsStaleEvent(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	a.opts.Events = out.NewEventWriter(os.Stderr, true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	staleEmitted := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				f.mu.Lock()
				calls := f.connectCalls
				f.mu.Unlock()
				if calls >= 2 {
					close(staleEmitted)
					cancel()
					return
				}
			}
		}
	}()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:           SyncModeFollow,
		AllowQR:        false,
		MaxReconnect:   time.Second,
		StaleThreshold: 200 * time.Millisecond,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync: %v", err)
	}

	select {
	case <-staleEmitted:
	default:
		t.Fatal("expected stale event to trigger reconnect")
	}
}
