package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// captureWarningCodes routes the app's warnings to a JSON event stream and
// returns a func listing the warning codes emitted so far.
func captureWarningCodes(t *testing.T, a *App) func() []string {
	t.Helper()
	var events lockedBuffer
	a.opts.Events = out.NewEventWriter(&events, true)
	return func() []string {
		events.mu.Lock()
		defer events.mu.Unlock()
		var codes []string
		for line := range bytes.SplitSeq(bytes.TrimSpace(events.buf.Bytes()), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var event struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatalf("invalid event JSON: %s: %v", line, err)
			}
			if code, ok := event.Data["code"].(string); ok {
				codes = append(codes, code)
			}
		}
		return codes
	}
}

func cursorVersion(t *testing.T, device *waStore.Device) uint64 {
	t.Helper()
	version, _, err := device.AppState.GetAppStateVersion(t.Context(), string(contactCollection))
	if err != nil {
		t.Fatal(err)
	}
	return version
}

// sdkPatchChain encodes valid consecutive contact patches from v80 with the
// pinned SDK, checks each decodes, and resets the receiver to v80.
func sdkPatchChain(t *testing.T, device *waStore.Device, proc *appstate.Processor, keys [][]byte, targets []types.JID, names []string) []*waServerSync.SyncdPatch {
	t.Helper()
	initial := appstate.HashState{Version: 80}
	state := initial
	patches := make([]*waServerSync.SyncdPatch, 0, len(targets))
	for i := range targets {
		patch := encodeContactPatch(t, proc, keys[i], state, uint64(81+i), targets[i], names[i], "")
		mutations, next, err := proc.DecodePatches(t.Context(), &appstate.PatchList{Name: contactCollection, Patches: []*waServerSync.SyncdPatch{patch}}, state, true)
		if err != nil || len(mutations) != 1 || next.Version != uint64(81+i) {
			t.Fatalf("invalid encrypted fixture %d: mutations=%d version=%d err=%v", i, len(mutations), next.Version, err)
		}
		patches = append(patches, patch)
		state = next
	}
	if err := device.AppState.DeleteAppStateVersion(t.Context(), string(contactCollection)); err != nil {
		t.Fatal(err)
	}
	if err := device.AppState.PutAppStateVersion(t.Context(), string(contactCollection), initial.Version, initial.Hash); err != nil {
		t.Fatal(err)
	}
	return patches
}

// A write for the saved contact during the send does not prove that what
// whatsmeow consumed meanwhile was applied: a fetch running during the send
// can store the version of a patch and then fail on a later one, dropping
// the names of the patches it stored, whichever contact they are for. The
// save keeps its debt whenever the cursor moved by more than one patch, or
// by one patch without the save's own name written after it. (Round-5
// review, codex-sol and codex-astra; patches encoded and applied with the
// pinned SDK over a real session store.)
func TestSaveContactKeepsDebtWhenTheSendWindowIsNotCovered(t *testing.T) {
	other := types.NewJID("15550002222", types.DefaultUserServer)
	third := types.NewJID("15550003333", types.DefaultUserServer)
	cases := []struct {
		name     string
		targets  []types.JID
		names    []string
		laterKey []bool
		// send runs the SDK steps of the schedule; apply applies a list of
		// patches from the stored cursor.
		send      func(apply func(...*waServerSync.SyncdPatch) error, deliver func(), patches []*waServerSync.SyncdPatch) error
		lost      types.JID
		lostName  string
		wantFound bool
	}{
		{
			// A notification fetch stores v81 (another contact) and fails on
			// v82; the send conflicts at v82, applies it, retries, and
			// fetches its own echo v83.
			name:     "another contact lost before our echo",
			targets:  []types.JID{other, third, testContactPN},
			names:    []string{"Grace Hopper", "Lin Wei", "Ada Lovelace"},
			laterKey: []bool{false, true, true},
			send: func(apply func(...*waServerSync.SyncdPatch) error, deliver func(), p []*waServerSync.SyncdPatch) error {
				if err := apply(p[0], p[1]); !errors.Is(err, appstate.ErrKeyNotFound) {
					return errors.Join(errors.New("test setup: background fetch did not fail on the later key"), err)
				}
				deliver()
				if err := apply(p[1]); err != nil {
					return err
				}
				return apply(p[2])
			},
			lost: other, lostName: "Grace Hopper",
		},
		{
			// A phone edit of the same contact is applied during the send;
			// a notification fetch then stores our own v82 and fails on v83.
			name:     "earlier same-index write mistaken for our echo",
			targets:  []types.JID{testContactPN, testContactPN, other},
			names:    []string{"Earlier Name", "Ada Lovelace", "Grace Hopper"},
			laterKey: []bool{false, false, true},
			send: func(apply func(...*waServerSync.SyncdPatch) error, deliver func(), p []*waServerSync.SyncdPatch) error {
				if err := apply(p[0]); err != nil {
					return err
				}
				if err := apply(p[1], p[2]); !errors.Is(err, appstate.ErrKeyNotFound) {
					return errors.Join(errors.New("test setup: background fetch did not fail on the later key"), err)
				}
				deliver()
				return apply(p[2])
			},
			lost: testContactPN, lostName: "Ada Lovelace",
		},
		{
			// Our own echo v81 is applied; a notification fetch stores v82
			// (another contact) and fails on v83, which the post-send fetch
			// then applies.
			name:     "real own echo does not cover an unrelated lost patch",
			targets:  []types.JID{testContactPN, other, third},
			names:    []string{"Ada Lovelace", "Grace Hopper", "Lin Wei"},
			laterKey: []bool{false, false, true},
			send: func(apply func(...*waServerSync.SyncdPatch) error, deliver func(), p []*waServerSync.SyncdPatch) error {
				if err := apply(p[0]); err != nil {
					return err
				}
				if err := apply(p[1], p[2]); !errors.Is(err, appstate.ErrKeyNotFound) {
					return errors.Join(errors.New("test setup: background fetch did not fail on the later key"), err)
				}
				deliver()
				return apply(p[2])
			},
			lost: other, lostName: "Grace Hopper",
		},
		{
			// Everything consumed during the send was applied, but the
			// journal cannot tell that apart from the cases above: the debt
			// is kept (conservative), and every name is mirrored.
			name:     "complete concurrent batch",
			targets:  []types.JID{testContactPN, other, third},
			names:    []string{"Ada Lovelace", "Grace Hopper", "Lin Wei"},
			laterKey: []bool{false, false, true},
			send: func(apply func(...*waServerSync.SyncdPatch) error, deliver func(), p []*waServerSync.SyncdPatch) error {
				if err := apply(p[0]); err != nil {
					return err
				}
				deliver()
				return apply(p[1], p[2])
			},
			lost: other, lostName: "Grace Hopper", wantFound: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			remove, err := a.AddChatStatePersistenceHandler(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(remove)
			key, laterKey := []byte{1, 2, 3}, []byte{9, 9, 9}
			device, sdk, proc := newSDKSession(t, f, key, 80)
			if err := device.AppStateKeys.PutAppStateSyncKey(t.Context(), laterKey, waStore.AppStateSyncKey{Data: bytes.Repeat([]byte{0xCD}, 32), Fingerprint: []byte{2}, Timestamp: 2}); err != nil {
				t.Fatal(err)
			}
			keys := make([][]byte, len(tc.targets))
			for i, later := range tc.laterKey {
				keys[i] = key
				if later {
					keys[i] = laterKey
				}
			}
			patches := sdkPatchChain(t, device, proc, keys, tc.targets, tc.names)
			withheld := &withheldKeyStore{AppStateSyncKeyStore: device.AppStateKeys, withheld: laterKey}
			device.AppStateKeys = withheld
			w := &sdkSessionWA{fakeWA: f, device: device}
			a.wa = w
			var delivered []any
			w.send = func(ctx context.Context) error {
				apply := func(batch ...*waServerSync.SyncdPatch) error {
					v, h, err := device.AppState.GetAppStateVersion(ctx, string(contactCollection))
					if err != nil {
						return err
					}
					var evts []any
					_, err = sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, appstate.HashState{Version: v, Hash: h}, &appstate.PatchList{Name: contactCollection, Patches: batch}, false, &evts)
					if err == nil {
						delivered = append(delivered, evts...)
					}
					return err
				}
				return tc.send(apply, func() { withheld.delivered.Store(true) }, patches)
			}
			warnings := captureWarningCodes(t, a)
			if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
				t.Fatal(err)
			}
			f.dispatchAsync(delivered...)
			if v := cursorVersion(t, device); v != 83 {
				t.Fatalf("test setup: final cursor = %d, want 83", v)
			}
			if got := storedContact(t, a, tc.lost); (got.full == tc.lostName) != tc.wantFound {
				t.Fatalf("test setup: %s mirrored as %+v", tc.lostName, got)
			}
			if !contactRecoveryRequired(t, a) {
				t.Fatal("contact debt settled although the send window is not known to be covered")
			}
			if codes := warnings(); !slices.Contains(codes, "contact_save_coverage_unknown") {
				t.Fatalf("warnings = %v, want contact_save_coverage_unknown", codes)
			}
		})
	}
}

// The healthy save: the cursor moves by exactly one patch during the send and
// the save's own name is written after it, so that patch's batch was applied
// and the debt is settled, whichever fetch applied it.
func TestSaveContactSettlesWhenOnlyItsOwnPatchWasConsumed(t *testing.T) {
	for _, tc := range []string{"post-send fetch", "notification fetch before an empty post-send fetch"} {
		t.Run(tc, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			remove, err := a.AddChatStatePersistenceHandler(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(remove)
			key := []byte{1, 2, 3}
			device, sdk, proc := newSDKSession(t, f, key, 80)
			patches := sdkPatchChain(t, device, proc, [][]byte{key}, []types.JID{testContactPN}, []string{"Ada Lovelace"})
			w := &sdkSessionWA{fakeWA: f, device: device}
			a.wa = w
			w.send = func(ctx context.Context) error {
				apply := func(batch ...*waServerSync.SyncdPatch) error {
					v, h, err := device.AppState.GetAppStateVersion(ctx, string(contactCollection))
					if err != nil {
						return err
					}
					var evts []any
					_, err = sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, appstate.HashState{Version: v, Hash: h}, &appstate.PatchList{Name: contactCollection, Patches: batch}, false, &evts)
					return err
				}
				if err := apply(patches[0]); err != nil {
					return err
				}
				if tc == "notification fetch before an empty post-send fetch" {
					return apply()
				}
				return nil
			}
			warnings := captureWarningCodes(t, a)
			if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
				t.Fatal(err)
			}
			if got := storedContact(t, a, testContactPN); got.full != "Ada Lovelace" {
				t.Fatalf("local mirror = %+v", got)
			}
			if contactRecoveryRequired(t, a) {
				t.Fatal("contact debt kept although only the save's own patch was consumed and it was applied")
			}
			if codes := warnings(); len(codes) != 0 {
				t.Fatalf("warnings = %v, want none", codes)
			}
		})
	}
}

// A post-send fetch that returned nothing consumed nothing: no name can have
// been lost, so the debt is settled, and the save warns that its name is not
// in the local index yet (the next fetch brings the echo).
func TestSaveContactWithNothingFetchedBackSettlesAndWarns(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.saveContactAfterSend = func(fakeSaveContactCall) {}
	warnings := captureWarningCodes(t, a)
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("contact debt kept although the send consumed nothing")
	}
	if got := storedContact(t, a, testContactPN); got.found {
		t.Fatalf("local mirror = %+v, but whatsmeow applied no name", got)
	}
	if codes := warnings(); !slices.Contains(codes, "contact_save_echo_pending") {
		t.Fatalf("warnings = %v, want contact_save_echo_pending", codes)
	}
}
