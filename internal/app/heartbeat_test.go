package app

import (
	"bytes"
	"context"
	"encoding/json"
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
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeOnce},
		&messagesStored,
		&lastEvent,
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
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow},
		&messagesStored,
		&lastEvent,
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

func TestSyncFollowDoesNotReconnectOnFreshKeepAliveTimeout(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	disconnected := make(chan struct{}, 1)
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow, StaleThreshold: time.Minute},
		&messagesStored,
		&lastEvent,
		disconnected,
		func(string, string) {},
		nil,
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	f.emit(&events.KeepAliveTimeout{ErrorCount: 1, LastSuccess: nowUTC()})

	select {
	case <-disconnected:
		t.Fatal("fresh keepalive timeout triggered reconnect")
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
	disconnected := make(chan struct{}, 1)
	handlerID := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow, StaleThreshold: 200 * time.Millisecond},
		&messagesStored,
		&lastEvent,
		disconnected,
		func(string, string) {},
		nil,
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	f.emit(&events.KeepAliveTimeout{ErrorCount: 2, LastSuccess: nowUTC().Add(-time.Minute)})

	select {
	case <-disconnected:
	default:
		t.Fatal("expected stale event to trigger reconnect")
	}
	if f.IsConnected() {
		t.Fatal("expected stale event to close connection before reconnect")
	}

	var envelope struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(eventsOut.Bytes()), &envelope); err != nil {
		t.Fatalf("parse stale event %q: %v", eventsOut.String(), err)
	}
	if envelope.Event != "stale" {
		t.Fatalf("event = %q, want stale", envelope.Event)
	}
	if envelope.Data["source"] != "keepalive_timeout" || envelope.Data["error_count"] != float64(2) {
		t.Fatalf("unexpected stale event data: %#v", envelope.Data)
	}
}
