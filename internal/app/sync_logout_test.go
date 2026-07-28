package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types/events"
)

// findLoggedOutEvent parses NDJSON --events output and returns the first event
// with the given name, failing the test if none is present.
func findEventByName(t *testing.T, raw, name string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("event line is not valid JSON %q: %v\nfull output:\n%s", line, err, raw)
		}
		if evt["event"] == name {
			return evt
		}
	}
	t.Fatalf("no %q event in:\n%s", name, raw)
	return nil
}

// A forced logout (device removed on the phone, or a ban) must be surfaced as a
// structured `logged_out` event and must signal the loggedOut channel so the
// follow loop can stop instead of reconnecting forever.
func TestSyncEventHandlerEmitsLoggedOutAndSignals(t *testing.T) {
	cases := []struct {
		name           string
		event          *events.LoggedOut
		wantOnConnect  bool
		wantReasonCode float64
	}{
		{"forced_logout", &events.LoggedOut{OnConnect: true, Reason: events.ConnectFailureLoggedOut}, true, 401},
		{"stream_error", &events.LoggedOut{OnConnect: false}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			var eventsOut bytes.Buffer
			a.opts.Events = out.NewEventWriter(&eventsOut, true)

			var messagesStored, lastEvent atomic.Int64
			loggedOut := make(chan struct{}, 1)
			handlerID := a.addSyncEventHandler(
				context.Background(),
				SyncOptions{Mode: SyncModeFollow},
				&messagesStored,
				&lastEvent,
				make(chan struct{}, 1),
				loggedOut,
				make(chan staleReconnectRequest, 1),
				func(string, string) {},
				nil,
				nil,
				&syncPresence{},
				nil,
			)
			defer f.RemoveEventHandler(handlerID)

			f.emit(tc.event)

			select {
			case <-loggedOut:
			default:
				t.Fatal("LoggedOut event did not signal the loggedOut channel")
			}

			evt := findEventByName(t, eventsOut.String(), "logged_out")
			data, ok := evt["data"].(map[string]any)
			if !ok {
				t.Fatalf("logged_out event has no data object: %#v", evt)
			}
			if data["on_connect"] != tc.wantOnConnect {
				t.Fatalf("logged_out on_connect = %v, want %v", data["on_connect"], tc.wantOnConnect)
			}
			if code, _ := data["reason_code"].(float64); code != tc.wantReasonCode {
				t.Fatalf("logged_out reason_code = %v, want %v", data["reason_code"], tc.wantReasonCode)
			}
			if s, _ := data["reason"].(string); s == "" {
				t.Fatal("logged_out reason string is empty")
			}
		})
	}
}

// The follow loop must stop (return nil) on a logout signal without attempting a
// reconnect. Uses a background context so the ONLY way to return is the logout
// signal — proving it, not context cancellation, ends the loop.
func TestRunSyncFollowStopsOnLoggedOut(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var messagesStored, connectionEpoch atomic.Int64
	disconnected := make(chan struct{}, 1)
	loggedOut := make(chan struct{}, 1)
	staleReconnect := make(chan staleReconnectRequest, 1)
	loggedOut <- struct{}{}

	done := make(chan error, 1)
	go func() {
		_, err := a.runSyncFollow(context.Background(), time.Second, SyncPresenceModeNormal, &messagesStored, &connectionEpoch, disconnected, loggedOut, staleReconnect)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runSyncFollow returned error on logout: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSyncFollow did not stop after logged_out signal")
	}

	f.mu.Lock()
	calls := f.connectCalls
	f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("logged-out follow loop reconnected (connectCalls=%d), want 0", calls)
	}
}

// The bootstrap/once idle loop must also stop on logout rather than reconnect.
func TestRunSyncUntilIdleStopsOnLoggedOut(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var messagesStored, lastEvent atomic.Int64
	lastEvent.Store(nowUTC().UnixNano())
	disconnected := make(chan struct{}, 1)
	loggedOut := make(chan struct{}, 1)
	loggedOut <- struct{}{}

	done := make(chan error, 1)
	go func() {
		// Large idleExit so the idle ticker never fires before the logout signal.
		_, err := a.runSyncUntilIdle(context.Background(), time.Hour, time.Second, SyncPresenceModeNormal, &messagesStored, &lastEvent, disconnected, loggedOut)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runSyncUntilIdle returned error on logout: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSyncUntilIdle did not stop after logged_out signal")
	}

	f.mu.Lock()
	calls := f.connectCalls
	f.mu.Unlock()
	if calls != 0 {
		t.Fatalf("logged-out idle loop reconnected (connectCalls=%d), want 0", calls)
	}
}

// End-to-end: a LoggedOut delivered while `sync --follow` is running stops the
// daemon cleanly (Sync returns nil) and never reconnects.
func TestSyncFollowStopsWhenLoggedOut(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	done := make(chan error, 1)
	go func() {
		_, err := a.Sync(context.Background(), SyncOptions{
			Mode: SyncModeFollow,
			AfterConnect: func(context.Context) error {
				f.emit(&events.LoggedOut{OnConnect: true, Reason: events.ConnectFailureLoggedOut})
				return nil
			},
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Sync --follow returned error on logout: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sync --follow did not stop after logout")
	}

	f.mu.Lock()
	calls := f.connectCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("connectCalls = %d after logout, want 1 (initial connect, no reconnect)", calls)
	}
}
