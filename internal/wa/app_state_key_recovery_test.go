package wa

import (
	"bytes"
	"encoding/json"
	"errors"
	appStore "github.com/openclaw/wacli/internal/store"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waSyncdSnapshotRecovery"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestAppStateKeyGuardScopesUnavailableKeysAndAcceptsLaterDelivery(t *testing.T) {
	state := newTestKeyStateStore(t)
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: state})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	keys := c.client.Store.AppStateKeys
	id := []byte{1, 2, 3}
	if err := keys.PutAppStateSyncKey(t.Context(), id, waStore.AppStateSyncKey{}); !errors.Is(err, ErrEmptyAppStateKeyShare) {
		t.Fatal(err)
	}
	if key, err := keys.GetAppStateSyncKey(t.Context(), []byte{4, 5, 6}); key != nil || err != nil {
		t.Fatalf("ordinary missing key changed: %v", err)
	}
	data := bytes.Repeat([]byte{0xAB}, 32)
	if err := keys.PutAppStateSyncKey(t.Context(), id, waStore.AppStateSyncKey{Data: data, Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	if unavailable, err := state.IsAppStateKeyUnavailable(t.Context(), c.LinkedJID(), id); err != nil || unavailable {
		t.Fatalf("valid share did not clear durable status: %v", err)
	}
	// Another empty share must not poison the usable key already stored.
	_ = keys.PutAppStateSyncKey(t.Context(), id, waStore.AppStateSyncKey{})
	key, err := keys.GetAppStateSyncKey(t.Context(), id)
	if err != nil || key == nil || !bytes.Equal(key.Data, data) {
		t.Fatalf("usable key was lost: %v", err)
	}
}

func TestKnownEmptyAppStateKeyEmitsRealSDKSyncError(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id := []byte{1, 2, 3}
	_ = c.client.Store.AppStateKeys.PutAppStateSyncKey(t.Context(), id, waStore.AppStateSyncKey{})
	var reported *events.AppStateSyncError
	c.AddEventHandler(func(evt any) {
		if v, ok := evt.(*events.AppStateSyncError); ok {
			reported = v
		}
	})
	patches := &appstate.PatchList{Name: appstate.WAPatchRegularLow, Snapshot: &waServerSync.SyncdSnapshot{
		Version: &waServerSync.SyncdVersion{Version: proto.Uint64(1)}, KeyID: &waServerSync.KeyId{ID: id},
	}}
	_, err = c.client.DangerousInternals().ApplyAppStatePatches(t.Context(), appstate.WAPatchRegularLow, appstate.HashState{}, patches, true, nil)
	if !errors.Is(err, ErrEmptyAppStateKeyShare) || errors.Is(err, appstate.ErrKeyNotFound) {
		t.Fatalf("wrong key classification: %v", err)
	}
	if reported == nil || reported.Name != appstate.WAPatchRegularLow || !errors.Is(reported.Error, ErrEmptyAppStateKeyShare) {
		t.Fatalf("missing scoped SDK sync error: %+v", reported)
	}
}

func TestRealSDKRecoveryRequiresUsableSnapshotKey(t *testing.T) {
	for _, usable := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable", true: "usable"}[usable], func(t *testing.T) {
			c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx := t.Context()
			id := []byte{1, 2, 3}
			var data []byte
			if usable {
				data = bytes.Repeat([]byte{0xAB}, 32)
			}
			c.client.DangerousInternals().HandleAppStateSyncKeyShare(ctx, &waE2E.AppStateSyncKeyShare{Keys: []*waE2E.AppStateSyncKey{appStateKeyShare(id, data)}})
			name := appstate.WAPatchRegularLow
			if err := c.client.Store.AppState.PutAppStateVersion(ctx, string(name), 80, [128]byte{}); err != nil {
				t.Fatal(err)
			}
			chat := types.NewJID("15550000001", types.DefaultUserServer)
			index, err := json.Marshal([]string{"archive", chat.String()})
			if err != nil {
				t.Fatal(err)
			}
			snapshot := &waSyncdSnapshotRecovery.SyncdSnapshotRecovery{
				CollectionName: proto.String(string(name)), Version: &waSyncdSnapshotRecovery.SyncdVersion{Version: proto.Uint64(81)}, CollectionLthash: make([]byte, 128),
				MutationRecords: []*waSyncdSnapshotRecovery.SyncdPlainTextRecord{{KeyID: id, Mac: bytes.Repeat([]byte{0xCD}, 32), Value: &waSyncAction.SyncActionData{
					Index: index, Version: proto.Int32(3), Value: &waSyncAction.SyncActionValue{Timestamp: proto.Int64(time.Now().UnixMilli()), ArchiveChatAction: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}},
				}}},
			}
			raw, err := proto.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var archived, complete bool
			c.AddEventHandler(func(evt any) {
				switch v := evt.(type) {
				case *events.Archive:
					archived = v.JID == chat && v.Action.GetArchived()
				case *events.AppStateSyncComplete:
					complete = v.Name == name && v.Version == 81 && v.Recovery
				}
			})
			c.client.DangerousInternals().HandleAppStateRecovery(ctx, "synthetic-recovery", []*waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult{{SyncdSnapshotFatalRecoveryResponse: &waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult_SyncDSnapshotFatalRecoveryResponse{CollectionSnapshot: raw}}})
			version, _, err := c.client.Store.AppState.GetAppStateVersion(ctx, string(name))
			if err != nil {
				t.Fatal(err)
			}
			settings, err := c.client.Store.ChatSettings.GetChatSettings(ctx, chat)
			if err != nil || settings.Archived != usable {
				t.Fatalf("snapshot SQL archive=%v usable=%v error=%v", settings.Archived, usable, err)
			}
			wantVersion := uint64(80)
			if usable {
				wantVersion = 81
			}
			if archived != usable || complete != usable || version != wantVersion {
				t.Fatalf("archive=%v complete=%v version=%d usable=%v", archived, complete, version, usable)
			}
		})
	}
}

func TestEmptyAppStateKeyStatusSurvivesClientReopen(t *testing.T) {
	path := newPairedSessionStore(t)
	statePath := filepath.Join(t.TempDir(), "wacli.db")
	state, err := appStore.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	c, err := New(Options{StorePath: path, KeyStateStore: state})
	if err != nil {
		t.Fatal(err)
	}
	id := []byte{9, 8, 7}
	if err := c.client.Store.AppStateKeys.PutAppStateSyncKey(t.Context(), id, waStore.AppStateSyncKey{}); !errors.Is(err, ErrEmptyAppStateKeyShare) {
		t.Fatal(err)
	}
	c.Close()
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedState, err := appStore.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedState.Close()
	reopened, err := New(Options{StorePath: path, KeyStateStore: reopenedState})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.client.Store.AppStateKeys.GetAppStateSyncKey(t.Context(), id); !errors.Is(err, ErrEmptyAppStateKeyShare) {
		t.Fatalf("empty-key status was lost after reopen: %v", err)
	}
}
