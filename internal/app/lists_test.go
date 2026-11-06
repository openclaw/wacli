package app

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func listFixtureEvents(at time.Time, jid types.JID) []any {
	return []any{
		&events.LabelEdit{LabelID: "7", Timestamp: at, FromFullSync: true, Action: &waSyncAction.LabelEditAction{
			Name: proto.String("Fixture"), Color: proto.Int32(3), PredefinedID: proto.Int32(0),
			Type: waSyncAction.LabelEditAction_CUSTOM.Enum(), OrderIndex: proto.Int32(2),
			IsActive: proto.Bool(true), IsImmutable: proto.Bool(false),
		}},
		&events.LabelAssociationChat{LabelID: "7", JID: jid, Timestamp: at, FromFullSync: true,
			Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(true)}},
	}
}

func TestListsLiveSyncAndOneShotPersistence(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		a := newTestApp(t)
		f := newFakeWA()
		a.wa = f
		lid := types.NewJID("999", types.HiddenUserServer)
		pn := types.NewJID("123", types.DefaultUserServer)
		f.lids[lid] = pn
		ctx := t.Context()
		if oneShot {
			remove, err := a.AddChatStatePersistenceHandler(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer remove()
		} else {
			var messages, last atomic.Int64
			a.addSyncEventHandler(ctx, SyncOptions{}, &messages, &last, make(chan struct{}, 1), make(chan struct{}, 1),
				make(chan staleReconnectRequest, 1), nil, nil, nil, &syncPresence{}, nil)
		}
		at := time.Unix(100, 0)
		for _, evt := range listFixtureEvents(at, lid) {
			f.emit(evt)
		}
		assertAppListMembers(t, a, pn.String())
		chat, err := a.db.GetChat(pn.String())
		if err != nil || !chat.LastMessageTS.IsZero() {
			t.Fatalf("label changed chat activity: %+v, %v", chat, err)
		}
		f.emit(&events.LabelEdit{LabelID: "7", Timestamp: at.Add(time.Second), Action: &waSyncAction.LabelEditAction{Name: proto.String("Renamed")}})
		f.emit(&events.LabelAssociationChat{LabelID: "7", JID: lid, Timestamp: at.Add(time.Second), Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(false)}})
		// Replaying an older snapshot cannot restore a removed membership.
		for _, evt := range listFixtureEvents(at, lid) {
			f.emit(evt)
		}
		assertAppListMembers(t, a)
		result, err := a.db.Lists()
		if err != nil || len(result.Lists) != 1 || result.Lists[0].Name != "Renamed" || !result.FullSyncEventsSeen {
			t.Fatalf("Lists = %+v, %v", result, err)
		}
		f.emit(&events.LabelEdit{LabelID: "7", Timestamp: at.Add(2 * time.Second), Action: &waSyncAction.LabelEditAction{Deleted: proto.Bool(true)}})
		result, err = a.db.Lists()
		if err != nil || len(result.Lists) != 0 {
			t.Fatalf("deleted definition visible: %+v, %v", result, err)
		}
		pending, err := a.db.AppStateRecoveryCollections()
		if err != nil || len(pending) != 0 {
			t.Fatalf("live recovery intents = %v, %v", pending, err)
		}
	}
}

func assertAppListMembers(t *testing.T, a *App, want ...string) {
	t.Helper()
	chats, err := a.db.ListChatsFiltered(store.ChatListFilter{ListID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, chat := range chats {
		got = append(got, chat.JID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
}

func TestListsRecoverySnapshotAndFetchedReplay(t *testing.T) {
	// Bound the waits so a routing regression fails fast instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	jid := types.NewJID("123", types.DefaultUserServer)
	fixture := listFixtureEvents(time.Unix(100, 0), jid)
	for _, evt := range fixture {
		if !reflect.DeepEqual(appStateCollectionsForEvent(evt), []appstate.WAPatchName{appstate.WAPatchRegular}) {
			t.Fatalf("wrong collection for %T", evt)
		}
	}
	f.onAppStateRecovery = func(name string) {
		for _, evt := range fixture {
			f.emit(evt)
		}
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Recovery: true})
	}
	for range 2 {
		evts, capture, err := a.waitForPrimaryAppStateRecovery(ctx, appstate.WAPatchRegular, nil)
		if err != nil || capture != nil {
			t.Fatalf("snapshot: %v, %v", capture, err)
		}
		if len(evts) != 2 {
			t.Fatalf("snapshot omitted labels: %v", evts)
		}
		if err := a.persistFetchedAppStateEvents(ctx, evts, &appStatePersistenceTracker{}); err != nil {
			t.Fatal(err)
		}
		assertAppListMembers(t, a, jid.String())
	}
	// Exercise the same FetchAppStateEvents boundary used by collection recovery.
	wrapped := &appStateContextWA{fakeWA: f}
	wrapped.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) { return fixture, nil }
	a.wa = wrapped
	for range 2 {
		fetchErr, persistErr := a.fetchAndPersistAppState(ctx, appstate.WAPatchRegular, true, &appStatePersistenceTracker{})
		if fetchErr != nil || persistErr != nil {
			t.Fatalf("full replay: %v, %v", fetchErr, persistErr)
		}
	}
	assertAppListMembers(t, a, jid.String())
}

func TestListsOrderedSnapshotDoesNotOvertakeNewerLiveRemoval(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	jid := types.NewJID("123", types.DefaultUserServer)
	at := time.Unix(100, 0)
	ticket := a.appStatePersist.reserve()
	a.handleAppStatePersistenceEvent(t.Context(), &events.LabelAssociationChat{LabelID: "7", JID: jid,
		Timestamp: at.Add(time.Second), Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(false)}}, nil)
	pending, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegular))
	if err != nil || !pending {
		t.Fatalf("queued event has no durable recovery marker: %t, %v", pending, err)
	}
	frontier := a.appStatePersist.complete(ticket, func() {
		if err := a.persistFetchedAppStateEvents(t.Context(), listFixtureEvents(at, jid), &appStatePersistenceTracker{}); err != nil {
			t.Error(err)
		}
	})
	if err := a.appStatePersist.waitThrough(t.Context(), frontier); err != nil {
		t.Fatal(err)
	}
	assertAppListMembers(t, a)
}

func TestListsStartupFullSyncIngestion(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	f.appStateFetchEvent = func(name string, full, onlyIfNotSynced bool) any {
		if name == string(appstate.WAPatchRegular) && full {
			for _, evt := range listFixtureEvents(time.Unix(100, 0), types.NewJID("123", types.DefaultUserServer)) {
				f.emit(evt)
			}
		}
		return nil
	}
	a.syncAppStateDeltas(t.Context(), &sync.Map{})
	assertAppListMembers(t, a, "123@s.whatsapp.net")
}

// Name resolution falls back to the raw JID when lookups fail; a label event
// must not write that fallback over a known name or do network lookups.
func TestListsAssociationKeepsKnownChatName(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	at := time.Unix(100, 0)
	group := types.NewJID("555", types.GroupServer)
	dm := types.NewJID("123", types.DefaultUserServer)
	for _, chat := range []types.JID{group, dm} {
		if err := a.db.UpsertChat(chat.String(), chatKind(chat), "Known Name", at); err != nil {
			t.Fatal(err)
		}
		f.emit(&events.LabelAssociationChat{LabelID: "7", JID: chat, Timestamp: at,
			Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(true)}})
		got, err := a.db.GetChat(chat.String())
		if err != nil || got.Name != "Known Name" {
			t.Fatalf("%s name = %q, %v", chatKind(chat), got.Name, err)
		}
	}
	if f.groupInfoCalls != 0 {
		t.Fatalf("label association did %d group lookups", f.groupInfoCalls)
	}
	// A removal for a chat that is not indexed must not create an empty row.
	unknown := types.NewJID("777", types.DefaultUserServer)
	f.emit(&events.LabelAssociationChat{LabelID: "7", JID: unknown, Timestamp: at,
		Action: &waSyncAction.LabelAssociationAction{Labeled: proto.Bool(false)}})
	if _, err := a.db.GetChat(unknown.String()); err == nil {
		t.Fatal("unlabel created a chat row")
	}
}

func TestListsPersistenceFailureReachesTrackerAndLiveCallback(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	// Closing the synthetic DB simulates storage failure without a real session.
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	tracker := &appStatePersistenceTracker{}
	fixture := listFixtureEvents(time.Unix(100, 0), types.NewJID("123", types.DefaultUserServer))
	if err := a.persistFetchedAppStateEvents(t.Context(), fixture, tracker); err == nil {
		t.Fatal("label persistence failure was not reported to the replay tracker")
	}
	// The tracker must not be poisoned by a previous batch.
	if err := a.persistFetchedAppStateEvents(t.Context(), nil, tracker); err != nil {
		t.Fatal(err)
	}
	var callbackErr error
	a.handleAppStatePersistenceEvent(t.Context(), fixture[0], func(err error) { callbackErr = err })
	if callbackErr == nil {
		t.Fatal("live persistence failure did not reach sync freshness tracking")
	}
	// Invalid typed events are ignored safely, not treated as a successful write.
	for _, evt := range []any{(*events.LabelEdit)(nil), (*events.LabelAssociationChat)(nil), &events.LabelEdit{}, &events.LabelAssociationChat{}} {
		if err := a.persistAppStateEvent(t.Context(), evt, nil); err != nil {
			t.Fatalf("invalid event %T: %v", evt, err)
		}
	}
}
