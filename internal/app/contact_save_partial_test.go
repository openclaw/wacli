package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// sdkSessionWA runs a save's network exchange as the pinned SDK's own
// application step (ApplyAppStatePatches over a real session sqlstore with
// the journal installed, MAC validation on); only the server's response is
// supplied by the test.
type sdkSessionWA struct {
	*fakeWA
	device *waStore.Device
	send   func(context.Context) error
}

func (w *sdkSessionWA) SendAppStatePatch(ctx context.Context, patch appstate.PatchInfo, beforeApply func()) ([]any, error) {
	beforeApply()
	return nil, w.send(ctx)
}

type withheldKeyStore struct {
	waStore.AppStateSyncKeyStore
	withheld  []byte
	delivered atomic.Bool
}

func (s *withheldKeyStore) GetAppStateSyncKey(ctx context.Context, id []byte) (*waStore.AppStateSyncKey, error) {
	if bytes.Equal(id, s.withheld) && !s.delivered.Load() {
		return nil, nil
	}
	return s.AppStateSyncKeyStore.GetAppStateSyncKey(ctx, id)
}

func newSDKSession(t *testing.T, f *fakeWA, keyID []byte, version uint64) (*waStore.Device, *whatsmeow.Client, *appstate.Processor) {
	t.Helper()
	ctx := t.Context()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := device.AppStateKeys.PutAppStateSyncKey(ctx, keyID, waStore.AppStateSyncKey{Data: bytes.Repeat([]byte{7}, 32), Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	if err := device.AppState.PutAppStateVersion(ctx, string(contactCollection), version, [128]byte{}); err != nil {
		t.Fatal(err)
	}
	f.journal = wa.NewContactStoreJournal(device.Contacts)
	device.Contacts = f.journal
	device.AppState = f.journal.AppStateStore(device.AppState)
	sdk := whatsmeow.NewClient(device, nil)
	return device, sdk, appstate.NewProcessor(device, waLog.Noop)
}

func encodeContactPatch(t *testing.T, proc *appstate.Processor, keyID []byte, from appstate.HashState, version uint64, target types.JID, full, first string) *waServerSync.SyncdPatch {
	t.Helper()
	info, err := wa.BuildSaveContact(target, testContactLID, full, first, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proc.EncodePatch(t.Context(), keyID, from, info)
	if err != nil {
		t.Fatal(err)
	}
	patch := &waServerSync.SyncdPatch{}
	if err := proto.Unmarshal(raw, patch); err != nil {
		t.Fatal(err)
	}
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(version)}
	return patch
}

// whatsmeow's DecodePatches stores each patch's version as it decodes a
// batch, and applyAppStatePatches applies the batch's names only once the
// whole batch decoded. When the 409 or post-send batch of a send fails on a
// later patch (a hash mismatch, a key not delivered yet), the cursor is past
// an authentic patch whose names were never applied or journaled, and no
// incremental fetch will return it again. The failed save must keep its
// recovery intent. (Round-4 review, codex-astra and codex-sol.)
func TestSaveContactFailedSendAfterPartialDecodeKeepsDebt(t *testing.T) {
	for _, failure := range []string{"later hash mismatch", "later key missing"} {
		t.Run(failure, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			remove, err := a.AddChatStatePersistenceHandler(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(remove)
			keyID := []byte{1, 2, 3, 4}
			device, sdk, proc := newSDKSession(t, f, keyID, 10)
			initial := appstate.HashState{Version: 10}
			other := types.NewJID("15550002222", types.DefaultUserServer)
			first := encodeContactPatch(t, proc, keyID, initial, 11, other, "Grace Hopper", "Grace")
			bad := proto.Clone(first).(*waServerSync.SyncdPatch)
			bad.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(12)}
			bad.SnapshotMAC = bytes.Repeat([]byte{0xff}, 32)
			wantErr := appstate.ErrMismatchingLTHash
			if failure == "later key missing" {
				bad.KeyID = &waServerSync.KeyId{ID: []byte{99, 99, 99, 99}}
				wantErr = appstate.ErrKeyNotFound
			}
			w := &sdkSessionWA{fakeWA: f, device: device}
			a.wa = w
			w.send = func(ctx context.Context) error {
				var evts []any
				_, err := sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, initial, &appstate.PatchList{
					Name: contactCollection, Patches: []*waServerSync.SyncdPatch{first, bad},
				}, false, &evts)
				if !errors.Is(err, wantErr) {
					t.Errorf("test setup: second patch did not fail with %v: %v", wantErr, err)
				}
				pending, _ := f.journal.Snapshot()
				if v, _, _ := device.AppState.GetAppStateVersion(ctx, string(contactCollection)); v != 11 || len(evts) != 0 || len(pending) != 0 {
					t.Errorf("test setup: cursor=%d events=%d journal=%d, want the first patch consumed without applying it", v, len(evts), len(pending))
				}
				return err
			}
			if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); !errors.Is(err, wantErr) {
				t.Fatalf("save error = %v", err)
			}
			if !contactRecoveryRequired(t, a) {
				t.Fatal("contact debt settled although the failed send's batch moved the cursor past names it never applied")
			}
		})
	}
}

// A successful send proves WhatsApp accepted the save, not that whatsmeow
// applied its echo here: a background fetch can consume the save's patch in
// a batch that then fails on a later patch (see above), and the post-send
// fetch, which starts from the stored cursor, then succeeds without it. The
// cursor moved past the save's patch without its name being applied, so the
// save keeps its recovery intent. (Round-4 review, codex-sol and
// codex-astra.)
func TestSaveContactWithoutItsOwnEchoKeepsDebt(t *testing.T) {
	t.Run("background partial decode consumed the echo", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		keyID, laterKey := []byte{1, 2, 3}, []byte{9, 9, 9}
		device, sdk, proc := newSDKSession(t, f, keyID, 80)
		state := appstate.HashState{Version: 80}
		own := encodeContactPatch(t, proc, keyID, state, 81, testContactPN, "Ada Lovelace", "Ada")
		_, afterOwn, err := proc.DecodePatches(t.Context(), &appstate.PatchList{Name: contactCollection, Patches: []*waServerSync.SyncdPatch{own}}, state, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := device.AppStateKeys.PutAppStateSyncKey(t.Context(), laterKey, waStore.AppStateSyncKey{Data: bytes.Repeat([]byte{0xCD}, 32), Fingerprint: []byte{2}, Timestamp: 2}); err != nil {
			t.Fatal(err)
		}
		other := encodeContactPatch(t, proc, laterKey, afterOwn, 82, types.NewJID("15550002222", types.DefaultUserServer), "Grace Hopper", "Grace")
		// Computing the intermediate state stored v81 and its MACs; start the
		// receiver from v80 again.
		if err := device.AppState.DeleteAppStateVersion(t.Context(), string(contactCollection)); err != nil {
			t.Fatal(err)
		}
		if err := device.AppState.PutAppStateVersion(t.Context(), string(contactCollection), state.Version, state.Hash); err != nil {
			t.Fatal(err)
		}
		keys := &withheldKeyStore{AppStateSyncKeyStore: device.AppStateKeys, withheld: laterKey}
		device.AppStateKeys = keys
		w := &sdkSessionWA{fakeWA: f, device: device}
		a.wa = w
		w.send = func(ctx context.Context) error {
			// A notification's fetch, after WhatsApp accepted the save as
			// patch 81: it stores v81 and fails on 82, whose key is not here.
			var evts []any
			if _, err := sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, state, &appstate.PatchList{
				Name: contactCollection, Patches: []*waServerSync.SyncdPatch{own, other},
			}, false, &evts); !errors.Is(err, appstate.ErrKeyNotFound) {
				t.Errorf("test setup: background fetch did not fail on the later key: %v", err)
				return errors.New("test setup failed")
			}
			// The key arrives; the send's post-send fetch starts from v81.
			keys.delivered.Store(true)
			version, hash, err := device.AppState.GetAppStateVersion(ctx, string(contactCollection))
			if err != nil || version != 81 {
				t.Errorf("test setup: cursor = %d, %v", version, err)
				return errors.New("test setup failed")
			}
			_, err = sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, appstate.HashState{Version: version, Hash: hash}, &appstate.PatchList{
				Name: contactCollection, Patches: []*waServerSync.SyncdPatch{other},
			}, false, &evts)
			return err
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		if !contactRecoveryRequired(t, a) {
			t.Fatal("contact debt settled although whatsmeow never applied the save's own mutation")
		}
	})
}
