package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		alreadyIn   bool
		events      []any
		cancel      bool
		wantRevoked bool
		wantErr     string
	}{
		{name: "fresh login", fresh: true, events: []any{&events.Connected{}}},
		{name: "confirmed reauthentication", events: []any{&events.Connected{}}},
		{name: "already logged in", alreadyIn: true},
		{name: "socket only", cancel: true, wantRevoked: true, wantErr: "context canceled"},
		{name: "rejected login", events: []any{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}}, wantRevoked: true, wantErr: "revoked"},
		{name: "fresh rejected login", fresh: true, events: []any{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}}, wantRevoked: true, wantErr: "revoked"},
		{name: "late connected after rejection", events: []any{&events.LoggedOut{Reason: events.ConnectFailureLoggedOut}, &events.Connected{}}, wantRevoked: true, wantErr: "revoked"},
		{name: "logged out connection failure", fresh: true, events: []any{&events.ConnectFailure{Reason: events.ConnectFailureMainDeviceGone}}, wantRevoked: true, wantErr: "login failed"},
		{name: "ordinary login failure", events: []any{&events.ConnectFailure{Reason: events.ConnectFailureServiceUnavailable}}, wantRevoked: true, wantErr: "login failed"},
		{name: "disconnected before login", events: []any{&events.Disconnected{}}, wantRevoked: true, wantErr: "disconnected"},
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
			f.connected = true
			f.loggedIn = tc.alreadyIn
			f.connect = func(context.Context, wa.ConnectOptions) error {
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
			if len(f.handlers) != 0 {
				t.Fatalf("Connect leaked %d event handlers", len(f.handlers))
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
