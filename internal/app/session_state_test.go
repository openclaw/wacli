package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types/events"
)

func TestSessionRevokedMarkerLifecycle(t *testing.T) {
	storeDir := t.TempDir()

	revoked, err := SessionRevoked(storeDir)
	if err != nil || revoked {
		t.Fatalf("initial SessionRevoked() = %v, %v; want false, nil", revoked, err)
	}
	if err := MarkSessionRevoked(storeDir, "logged_out"); err != nil {
		t.Fatalf("MarkSessionRevoked: %v", err)
	}
	revoked, err = SessionRevoked(storeDir)
	if err != nil || !revoked {
		t.Fatalf("marked SessionRevoked() = %v, %v; want true, nil", revoked, err)
	}
	if err := ClearSessionRevoked(storeDir); err != nil {
		t.Fatalf("ClearSessionRevoked: %v", err)
	}
	revoked, err = SessionRevoked(storeDir)
	if err != nil || revoked {
		t.Fatalf("cleared SessionRevoked() = %v, %v; want false, nil", revoked, err)
	}
}

type handshakeWA struct {
	*fakeWA
	connect func(context.Context, wa.ConnectOptions) error
}

func (f *handshakeWA) Connect(ctx context.Context, opts wa.ConnectOptions) error {
	return f.connect(ctx, opts)
}

func TestConnectRequiresConfirmedLoginBeforeClearingRevocation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fresh       bool
		alreadyOpen bool
		events      []any
		cancel      bool
		wantRevoked bool
		wantErr     string
	}{
		{name: "fresh login", fresh: true, events: []any{&events.Connected{}}},
		{name: "confirmed reauthentication", events: []any{&events.Connected{}}},
		{name: "already open connection", alreadyOpen: true, cancel: true, wantRevoked: true, wantErr: "context canceled"},
		{name: "cached prior login on reopened socket", cancel: true, wantRevoked: true, wantErr: "context canceled"},
		{name: "socket only", cancel: true, wantRevoked: true, wantErr: "context canceled"},
		{name: "rejected login", events: []any{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}}, wantRevoked: true, wantErr: "revoked"},
		{name: "disconnected then revoked", events: []any{&events.Disconnected{}, &events.LoggedOut{Reason: events.ConnectFailureLoggedOut}}, wantRevoked: true, wantErr: "revoked"},
		{name: "fresh rejected login", fresh: true, events: []any{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}}, wantRevoked: true, wantErr: "revoked"},
		{name: "late connected after rejection", events: []any{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, &events.Connected{}}, wantRevoked: true, wantErr: "revoked"},
		{name: "logged out connection failure", fresh: true, events: []any{&events.ConnectFailure{Reason: events.ConnectFailureMainDeviceGone}}, wantRevoked: true, wantErr: "login failed"},
		{name: "ordinary login failure", events: []any{&events.ConnectFailure{Reason: events.ConnectFailureServiceUnavailable}}, wantRevoked: true, wantErr: "login failed"},
		{name: "outdated client", events: []any{&events.ClientOutdated{}}, cancel: true, wantRevoked: true, wantErr: "outdated"},
		{name: "temporary ban", events: []any{&events.TemporaryBan{Code: events.TempBanBlockedByUsers}}, cancel: true, wantRevoked: true, wantErr: "temporarily banned"},
		{name: "stream replaced", events: []any{&events.StreamReplaced{}}, cancel: true, wantRevoked: true, wantErr: "replaced"},
		{name: "token refresh failed", events: []any{&events.CATRefreshError{Error: errors.New("synthetic refresh failure")}}, cancel: true, wantRevoked: true, wantErr: "authentication token"},
		{name: "stream error", events: []any{&events.StreamError{Code: "synthetic"}}, cancel: true, wantRevoked: true, wantErr: "stream error"},
		{name: "manual login reconnect", events: []any{&events.ManualLoginReconnect{}}, cancel: true, wantRevoked: true, wantErr: "reconnect"},
		{name: "disconnected before login", events: []any{&events.Disconnected{}}, cancel: true, wantRevoked: true, wantErr: "context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			if !tc.fresh {
				if err := MarkSessionRevoked(a.StoreDir(), "previous logout"); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f := &handshakeWA{fakeWA: newFakeWA()}
			f.connected = tc.alreadyOpen
			f.connect = func(context.Context, wa.ConnectOptions) error {
				f.connected = true
				for _, evt := range tc.events {
					f.emit(evt)
				}
				if tc.cancel {
					cancel()
				}
				return nil
			}
			a.wa = f
			err := a.Connect(ctx, false, nil)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Connect error = %v, want %q", err, tc.wantErr)
			}
			if got, err := SessionRevoked(a.StoreDir()); err != nil || got != tc.wantRevoked {
				t.Fatalf("SessionRevoked = %v, %v; want %v", got, err, tc.wantRevoked)
			}
			if len(f.handlers) != 1 {
				t.Fatalf("session observer count = %d, want 1", len(f.handlers))
			}
			a.Close()
			if len(f.handlers) != 0 {
				t.Fatalf("Close leaked %d event handlers", len(f.handlers))
			}
		})
	}
}

func TestSyncKeepsRevocationAfterSocketReturnsAndLateConnected(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.connectEvents = []any{
		&events.LoggedOut{Reason: events.ConnectFailureLoggedOut},
		&events.Connected{},
	}
	a.wa = f
	if _, err := a.Sync(t.Context(), SyncOptions{Mode: SyncModeOnce}); err != nil {
		t.Fatalf("shipped successful-stop contract changed: %v", err)
	}
	if revoked, err := SessionRevoked(a.StoreDir()); err != nil || !revoked {
		t.Fatalf("SessionRevoked = %v, %v; want true", revoked, err)
	}
}

func TestConfirmedLoginReportsMarkerCleanupFailure(t *testing.T) {
	a := newTestApp(t)
	marker := filepath.Join(a.StoreDir(), sessionRevokedFilename)
	if err := os.Mkdir(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, "blocked"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.wa = newFakeWA()
	if err := a.Connect(t.Context(), false, nil); err == nil || !strings.Contains(err.Error(), "clear session revoked marker") {
		t.Fatalf("Connect error = %v, want marker cleanup failure", err)
	}
}

func TestConnectKeepsObservingAfterPrecursorDisconnect(t *testing.T) {
	a := newTestApp(t)
	synctest.Test(t, func(t *testing.T) {
		f := &handshakeWA{fakeWA: newFakeWA()}
		f.connect = func(context.Context, wa.ConnectOptions) error {
			f.emit(&events.Disconnected{})
			return nil
		}
		a.wa = f
		done := make(chan error, 1)
		go func() { done <- a.Connect(t.Context(), false, nil) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("Connect stopped observing before the terminal logout: %v", err)
		default:
		}
		f.emit(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
		synctest.Wait()
		if err := <-done; err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("Connect error = %v, want revocation", err)
		}
		if revoked, err := SessionRevoked(a.StoreDir()); err != nil || !revoked {
			t.Fatalf("SessionRevoked = %v, %v; want true", revoked, err)
		}
		a.Close()
	})
}

func TestSessionObserverOutlivesLoginWait(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	if err := a.Connect(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	f.emit(&events.LoggedOut{Reason: events.ConnectFailureLoggedOut})
	if revoked, err := SessionRevoked(a.StoreDir()); err != nil || !revoked {
		t.Fatalf("post-login revocation = %v, %v; want true", revoked, err)
	}
	if err := a.Connect(t.Context(), false, nil); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("reusing revoked client returned %v", err)
	}
}

func TestReusingConfirmedConnectionDoesNotClearMarkerAgain(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	if err := a.Connect(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := MarkSessionRevoked(a.StoreDir(), "recorded after login"); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(t.Context(), false, nil); err != nil {
		t.Fatal(err)
	}
	if revoked, err := SessionRevoked(a.StoreDir()); err != nil || !revoked {
		t.Fatalf("connection reuse cleared marker: %v, %v", revoked, err)
	}
	if f.connectCalls != 1 || len(f.handlers) != 1 {
		t.Fatalf("reuse duplicated connection/observer: calls=%d handlers=%d", f.connectCalls, len(f.handlers))
	}
}

func TestConcurrentConnectWaitRespectsCancellation(t *testing.T) {
	a := newTestApp(t)
	synctest.Test(t, func(t *testing.T) {
		f := &handshakeWA{fakeWA: newFakeWA()}
		f.connect = func(context.Context, wa.ConnectOptions) error { return nil }
		a.wa = f
		done := make(chan error, 1)
		go func() { done <- a.Connect(t.Context(), false, nil) }()
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := a.Connect(ctx, false, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("queued Connect error = %v, want cancellation", err)
		}
		f.emit(&events.Connected{})
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		a.Close()
	})
}

type reconnectSnapshotWA struct {
	*fakeWA
	readConnected func() bool
}

func (f *reconnectSnapshotWA) IsConnected() bool { return f.readConnected() }

func TestConnectDoesNotDiscardConcurrentReconnectConfirmation(t *testing.T) {
	a := newTestApp(t)
	f := &reconnectSnapshotWA{fakeWA: newFakeWA()}
	a.wa = f
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	snapshotTaken := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	f.readConnected = func() bool {
		close(snapshotTaken)
		<-releaseSnapshot
		return false
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, false, nil) }()
	<-snapshotTaken
	f.mu.Lock()
	f.connected = true
	f.mu.Unlock()
	delivered := make(chan struct{})
	go func() {
		f.emit(&events.Connected{})
		close(delivered)
	}()
	// With an atomic snapshot, delivery waits for the state lock. The old path
	// delivered first, then erased confirmation using its stale false snapshot.
	select {
	case <-delivered:
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSnapshot)
	<-delivered
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("Connect lost the concurrent login confirmation")
	}
}
