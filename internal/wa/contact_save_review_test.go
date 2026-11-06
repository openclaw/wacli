package wa

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Ported from our independent branch, adapted to #500's builder and deliberate
// default-first-name / opt-in-phonebook semantics.
func TestReview500ContactPatchRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	d := c.NewDevice()
	d.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	d.Account = testAccount()
	if err := d.Save(ctx); err != nil {
		t.Fatal(err)
	}
	keyID := []byte{1, 2, 3, 4}
	if err := d.AppStateKeys.PutAppStateSyncKey(ctx, keyID, store.AppStateSyncKey{Data: bytes.Repeat([]byte{7}, 32), Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	proc := appstate.NewProcessor(d, waLog.Noop)
	pn := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("987654321098765", types.HiddenUserServer)
	for _, phonebook := range []bool{false, true} {
		// DecodePatches persists version/MAC state; each variant starts fresh.
		if err := d.AppState.DeleteAppStateVersion(ctx, string(appstate.WAPatchCriticalUnblockLow)); err != nil {
			t.Fatal(err)
		}
		patchInfo, err := BuildSaveContact(pn, lid, "Ada Lovelace", "", phonebook)
		if err != nil {
			t.Fatal(err)
		}
		before := time.Now().UnixMilli()
		raw, err := proc.EncodePatch(ctx, keyID, appstate.HashState{}, patchInfo)
		if err != nil {
			t.Fatal(err)
		}
		var patch waServerSync.SyncdPatch
		if err := proto.Unmarshal(raw, &patch); err != nil {
			t.Fatal(err)
		}
		patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
		mutations, _, err := proc.DecodePatches(ctx, &appstate.PatchList{Name: appstate.WAPatchCriticalUnblockLow, Patches: []*waServerSync.SyncdPatch{&patch}}, appstate.HashState{}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(mutations) != 1 {
			t.Fatalf("mutations = %d, want 1", len(mutations))
		}
		m := mutations[0]
		act := m.Action.GetContactAction()
		if m.Operation != waServerSync.SyncdMutation_SET || m.Version != 2 || len(m.Index) != 2 || m.Index[0] != appstate.IndexContact || m.Index[1] != pn.String() {
			t.Fatalf("decoded mutation = %+v", m)
		}
		if act.GetFullName() != "Ada Lovelace" || act.GetFirstName() != "Ada" || act.GetPnJID() != pn.String() || act.GetLidJID() != lid.String() || act.SaveOnPrimaryAddressbook == nil || act.GetSaveOnPrimaryAddressbook() != phonebook {
			t.Fatalf("decoded action (phonebook=%v) = %v", phonebook, act)
		}
		if m.Action.GetTimestamp() < before {
			t.Fatal("encoder did not fill timestamp")
		}
	}
}

func TestReview500ContactNamesRejectControls(t *testing.T) {
	pn := types.NewJID("15550001111", types.DefaultUserServer)
	for _, tc := range []struct{ full, first string }{{"Ada\nLovelace", ""}, {"Ada", "A\x00"}} {
		if _, err := BuildSaveContact(pn, types.JID{}, tc.full, tc.first, false); err == nil {
			t.Errorf("control characters accepted: full=%q first=%q", tc.full, tc.first)
		}
	}
}
