package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
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
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeOnce},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
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
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
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

func TestHeartbeatRetriesAfterWriteFailureInterval(t *testing.T) {
	a := newTestApp(t)
	storeDir := filepath.Join(t.TempDir(), "missing", "store")
	a.opts.StoreDir = storeDir

	captureStderr(t, func() {
		a.writeHeartbeat()
	})
	if got := a.heartbeatLast.Load(); got == 0 {
		t.Fatalf("heartbeatLast after failed write = %d, want throttled attempt timestamp", got)
	}
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	a.writeHeartbeat()
	if _, err := os.Stat(filepath.Join(storeDir, "HEARTBEAT")); !os.IsNotExist(err) {
		t.Fatalf("stat immediate retry err = %v, want throttled missing heartbeat", err)
	}

	a.heartbeatLast.Store(nowUTC().Add(-heartbeatMinInterval).UnixNano())
	a.writeHeartbeat()

	if _, err := os.Stat(filepath.Join(storeDir, "HEARTBEAT")); err != nil {
		t.Fatalf("stat heartbeat after retry: %v", err)
	}
}

func TestSyncFollowDoesNotReconnectOnFreshKeepAliveTimeout(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	disconnected := make(chan struct{}, 1)
	staleReconnect := make(chan staleReconnectRequest, 1)
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow, StaleThreshold: time.Minute},
		&messagesStored,
		&lastEvent,
		disconnected,
		staleReconnect,
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	f.emit(&events.KeepAliveTimeout{ErrorCount: 1, LastSuccess: nowUTC()})

	select {
	case req := <-staleReconnect:
		t.Fatalf("fresh keepalive timeout queued stale reconnect: %+v", req)
	case <-time.After(50 * time.Millisecond):
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
	var eventsOut bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventsOut, true)
	f.connected = true

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	var connectionEpoch atomic.Int64
	disconnected := make(chan struct{}, 1)
	staleReconnect := make(chan staleReconnectRequest, 1)
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow, StaleThreshold: 200 * time.Millisecond},
		&messagesStored,
		&lastEvent,
		disconnected,
		staleReconnect,
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.runSyncFollow(ctx, time.Second, &messagesStored, &connectionEpoch, disconnected, staleReconnect)
		done <- err
	}()

	f.emit(&events.KeepAliveTimeout{ErrorCount: 2, LastSuccess: nowUTC().Add(-time.Minute)})

	for {
		f.mu.Lock()
		connectCalls := f.connectCalls
		f.mu.Unlock()
		if connectCalls >= 1 {
			cancel()
			break
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("runSyncFollow: %v", err)
			}
			t.Fatal("expected stale event to reconnect")
		case <-time.After(10 * time.Millisecond):
		}
	}
	<-done

	var envelope struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	var found bool
	for _, line := range bytes.Split(bytes.TrimSpace(eventsOut.Bytes()), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var candidate struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(line, &candidate); err != nil {
			t.Fatalf("parse event line %q: %v\nfull events:\n%s", string(line), err, eventsOut.String())
		}
		if candidate.Event == "stale" {
			envelope = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing stale event in:\n%s", eventsOut.String())
	}
	if envelope.Data["source"] != "keepalive_timeout" || envelope.Data["error_count"] != float64(2) {
		t.Fatalf("unexpected stale event data: %#v", envelope.Data)
	}
}
