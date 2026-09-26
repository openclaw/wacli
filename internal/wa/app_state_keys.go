package wa

import (
	"bytes"
	"context"
	"errors"

	"go.mau.fi/whatsmeow/store"
)

// AppStateKeyUnavailable is dispatched through the client's event handlers when
// the primary device answers an app state key request with a share that has no
// key data. The primary no longer holds that key, so asking again returns the
// same empty share; collections encrypted with it can only be rebuilt from a
// recovery snapshot.
type AppStateKeyUnavailable struct {
	KeyID []byte
}

// ErrEmptyAppStateKeyShare is what whatsmeow logs for such a share instead of
// the "NOT NULL constraint failed: whatsmeow_app_state_sync_keys.key_data"
// error the SQL store would return.
var ErrEmptyAppStateKeyShare = errors.New("primary device shared the key without key data")

// appStateKeyGuard keeps key shares without key data out of the session store
// and reports them, because whatsmeow stores every received share as-is.
type appStateKeyGuard struct {
	store.AppStateSyncKeyStore
	onEmpty func(keyID []byte)
}

func (g *appStateKeyGuard) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	if len(key.Data) == 0 {
		if g.onEmpty != nil {
			g.onEmpty(bytes.Clone(id))
		}
		return ErrEmptyAppStateKeyShare
	}
	return g.AppStateSyncKeyStore.PutAppStateSyncKey(ctx, id, key)
}

// guardAppStateKeys wraps the device's key store once. Pairing replaces every
// session store on the device, so this runs again after PairSuccess.
func guardAppStateKeys(device *store.Device, onEmpty func(keyID []byte)) {
	if device == nil || device.AppStateKeys == nil {
		return
	}
	if _, ok := device.AppStateKeys.(*appStateKeyGuard); ok {
		return
	}
	device.AppStateKeys = &appStateKeyGuard{AppStateSyncKeyStore: device.AppStateKeys, onEmpty: onEmpty}
}
