package wa

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"go.mau.fi/whatsmeow/store"
)

// AppStateKeyUnavailable is dispatched through the client's event handlers when
// the primary device shares an app state key without data. Collections that
// cannot read that key use bounded snapshot recovery on their next fetch.
type AppStateKeyUnavailable struct {
	KeyID []byte
}

// ErrEmptyAppStateKeyShare is what whatsmeow logs for such a share instead of
// the "NOT NULL constraint failed: whatsmeow_app_state_sync_keys.key_data"
// error the SQL store would return.
var ErrEmptyAppStateKeyShare = errors.New("primary device shared the key without key data")

// AppStateKeyStateStore persists recovery observations without storing key material.
type AppStateKeyStateStore interface {
	MarkAppStateKeyUnavailable(context.Context, string, []byte) error
	ClearAppStateKeyUnavailable(context.Context, string, []byte) error
	IsAppStateKeyUnavailable(context.Context, string, []byte) (bool, error)
}

type appStateKeyGuard struct {
	store.AppStateSyncKeyStore
	onEmpty func(keyID []byte)
	mu      sync.Mutex
	account string
	state   AppStateKeyStateStore
}

func (g *appStateKeyGuard) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	g.mu.Lock()
	if len(key.Data) == 0 {
		existing, err := g.AppStateSyncKeyStore.GetAppStateSyncKey(ctx, id)
		if err != nil {
			g.mu.Unlock()
			return err
		}
		if existing == nil || len(existing.Data) == 0 {
			if g.state == nil {
				g.mu.Unlock()
				return fmt.Errorf("app-state key recovery store is not configured")
			}
			if err := g.state.MarkAppStateKeyUnavailable(ctx, g.account, id); err != nil {
				g.mu.Unlock()
				return fmt.Errorf("record unavailable app-state key: %w", err)
			}
		}
		g.mu.Unlock()
		if g.onEmpty != nil {
			g.onEmpty(bytes.Clone(id))
		}
		return ErrEmptyAppStateKeyShare
	}
	err := g.AppStateSyncKeyStore.PutAppStateSyncKey(ctx, id, key)
	if err == nil && g.state != nil {
		err = g.state.ClearAppStateKeyUnavailable(ctx, g.account, id)
	}
	g.mu.Unlock()
	return err
}

// Ordinary missing keys remain (nil,nil), so whatsmeow requests them normally.
// Known empty keys use a distinct error to trigger collection-scoped recovery.
// Usable key material always takes precedence over recovery metadata.
func (g *appStateKeyGuard) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key, err := g.AppStateSyncKeyStore.GetAppStateSyncKey(ctx, id)
	if err != nil {
		return nil, err
	}
	if key != nil {
		if len(key.Data) == 0 {
			return nil, ErrEmptyAppStateKeyShare
		}
		return key, nil
	}
	if g.state != nil {
		unavailable, err := g.state.IsAppStateKeyUnavailable(ctx, g.account, id)
		if err != nil {
			return nil, err
		}
		if unavailable {
			return nil, ErrEmptyAppStateKeyShare
		}
	}
	return nil, nil
}

// guardAppStateKeys wraps the device's key store once. Pairing replaces every
// session store on the device, so this runs again after PairSuccess.
func guardAppStateKeys(device *store.Device, state AppStateKeyStateStore, onEmpty func(keyID []byte)) {
	if device == nil || device.AppStateKeys == nil {
		return
	}
	if _, ok := device.AppStateKeys.(*appStateKeyGuard); ok {
		return
	}
	account := ""
	if device.ID != nil {
		account = device.ID.ToNonAD().String()
	}
	device.AppStateKeys = &appStateKeyGuard{AppStateSyncKeyStore: device.AppStateKeys, onEmpty: onEmpty, state: state, account: account}
}
