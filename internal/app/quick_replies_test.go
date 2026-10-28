package app

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func quickReplyEvent(id, shortcut, message string, at time.Time, deleted bool) *events.AppState {
	return &events.AppState{Index: []string{appstate.IndexQuickReply, id}, SyncActionValue: &waSyncAction.SyncActionValue{Timestamp: proto.Int64(at.UnixMilli()), QuickReplyAction: &waSyncAction.QuickReplyAction{Shortcut: proto.String(shortcut), Message: proto.String(message), Keywords: []string{"k"}, AssociatedLabelIDs: []string{"7"}, Count: proto.Int32(3), Deleted: proto.Bool(deleted)}}}
}

func newQuickWriteApp(t *testing.T, seed ...any) (*App, *fakeWA) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	if err := a.persistFetchedAppStateEvents(t.Context(), seed, &appStatePersistenceTracker{}); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func onlyQuickPatch(t *testing.T, f *fakeWA) appstate.MutationInfo {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appStatePatches) != 1 {
		t.Fatalf("patches=%d", len(f.appStatePatches))
	}
	p := f.appStatePatches[0]
	if p.Type != appstate.WAPatchRegular || p.Timestamp.IsZero() || len(p.Mutations) != 1 {
		t.Fatalf("patch=%+v", p)
	}
	return p.Mutations[0]
}

func TestQuickReplyWritesBuildRegularPatches(t *testing.T) {
	a, f := newQuickWriteApp(t)
	at := time.Unix(1700000000, 0)
	defer func(prev func() time.Time) { nowUTC = prev }(nowUTC)
	nowUTC = func() time.Time { return at }
	q, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("hello"), Message: proto.String("A"), Keywords: &[]string{"k"}})
	if err != nil {
		t.Fatal(err)
	}
	m := onlyQuickPatch(t, f)
	act := m.Value.GetQuickReplyAction()
	if m.Version != 3 || !reflect.DeepEqual(m.Index, []string{appstate.IndexQuickReply, "1700000000"}) || act.GetMessage() != "A" || !reflect.DeepEqual(act.GetKeywords(), []string{"k"}) || q.ID != "1700000000" {
		t.Fatalf("mutation=%+v value=%v", m, act)
	}
	if _, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("/hello"), Message: proto.String("dup")}); err == nil {
		t.Fatal("duplicate allowed")
	}
	f.appStatePatches = nil
	if q, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("second"), Message: proto.String("B")}); err != nil || q.ID != "1700000001" {
		t.Fatalf("collision=%+v %v", q, err)
	}
	f.appStatePatches = nil
	if _, err := a.EditQuickReply(t.Context(), "/hello", QuickReplyInput{Message: proto.String("edited")}); err != nil {
		t.Fatal(err)
	}
	if act := onlyQuickPatch(t, f).Value.GetQuickReplyAction(); act.GetMessage() != "edited" || !reflect.DeepEqual(act.GetKeywords(), []string{"k"}) {
		t.Fatalf("edit=%v", act)
	}
	f.appStatePatches = nil
	if _, err := a.DeleteQuickReply(t.Context(), q.ID); err != nil {
		t.Fatal(err)
	}
	if act := onlyQuickPatch(t, f).Value.GetQuickReplyAction(); !act.GetDeleted() || act.GetMessage() != "edited" {
		t.Fatalf("delete=%v", act)
	}
	f.appStatePatches = nil
	q, err = a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("third"), Message: proto.String("C")})
	if err != nil || q.ID != "1700000002" {
		t.Fatalf("tombstone ID reused=%+v %v", q, err)
	}
}

func TestQuickReplyClockSkewAndFollowingEdit(t *testing.T) {
	at := time.Unix(1700000000, 0)
	a, f := newQuickWriteApp(t, quickReplyEvent("1", "hello", "old", at.Add(time.Hour), false))
	defer func(prev func() time.Time) { nowUTC = prev }(nowUTC)
	nowUTC = func() time.Time { return at }
	if _, err := a.EditQuickReply(t.Context(), "1", QuickReplyInput{Message: proto.String("accepted")}); err != nil {
		t.Fatal(err)
	}
	if q, _, err := a.db.QuickReplyByID("1"); err != nil || q.Message != "accepted" {
		t.Fatalf("accepted=%+v %v", q, err)
	}
	f.appStateFetchEvent = func(string, bool, bool) any { return quickReplyEvent("1", "hello", "accepted", at, false) }
	f.appStatePatches = nil
	if _, err := a.EditQuickReply(t.Context(), "1", QuickReplyInput{Keywords: &[]string{"next"}}); err != nil {
		t.Fatal(err)
	}
	if act := onlyQuickPatch(t, f).Value.GetQuickReplyAction(); act.GetMessage() != "accepted" {
		t.Fatalf("resurrected=%v", act)
	}
	if pending, err := a.db.AppStateRecoveryRequired("regular"); err != nil || !pending {
		t.Fatalf("write debt=%v %v", pending, err)
	}
}

func TestQuickReplySyncAndOneShotIngestion(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		a, f := newQuickWriteApp(t)
		if oneShot {
			remove, err := a.AddChatStatePersistenceHandler(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer remove()
		} else {
			var msgs, last atomic.Int64
			a.addSyncEventHandler(t.Context(), SyncOptions{}, &msgs, &last, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1), nil, nil, nil, &syncPresence{}, nil)
		}
		evt := quickReplyEvent("1", "hello", "from phone", time.Unix(100, 0), false)
		if !reflect.DeepEqual(appStateCollectionsForEvent(evt), []appstate.WAPatchName{appstate.WAPatchRegular}) {
			t.Fatal("wrong recovery collection")
		}
		f.emit(evt)
		q, found, err := a.db.QuickReplyByID("1")
		if err != nil || !found || q.Message != "from phone" || *q.Count != 3 || !reflect.DeepEqual(q.LabelIDs, []string{"7"}) {
			t.Fatalf("inbound=%+v %v", q, err)
		}
		f.emit(quickReplyEvent("1", "hello", "", time.Unix(90, 0), true))
		if got, err := a.db.QuickReplies(); err != nil || len(got.QuickReplies) != 0 || got.Completeness != "incomplete" {
			t.Fatalf("inbound delete=%+v %v", got, err)
		}
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
		if got, err := a.db.QuickReplies(); err != nil || got.Completeness != "complete" {
			t.Fatalf("full sync=%+v %v", got, err)
		}
	}
}

func TestQuickReplyDuplicateAndInputErrorsFailClosed(t *testing.T) {
	for _, broken := range []bool{false, true} {
		a, f := newQuickWriteApp(t, quickReplyEvent("1", "dup", "A", time.Unix(1, 0), false), quickReplyEvent("2", "/dup", "B", time.Unix(1, 0), false))
		if broken {
			raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`UPDATE quick_replies SET keywords='bad json' WHERE id='1'`); err != nil {
				t.Fatal(err)
			}
			raw.Close()
		}
		if _, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("dup"), Message: proto.String("new")}); err == nil {
			t.Fatal("resolution error ignored")
		}
		if len(f.appStatePatches) != 0 {
			t.Fatal("invalid create sent")
		}
	}
	a, f := newQuickWriteApp(t)
	for _, in := range []QuickReplyInput{{Shortcut: proto.String("/"), Message: proto.String("x")}, {Shortcut: proto.String(" a"), Message: proto.String("x")}, {Shortcut: proto.String("a"), Message: proto.String(" ")}, {Shortcut: proto.String("a"), Message: proto.String("x"), Keywords: &[]string{""}}, {Shortcut: proto.String("a")}} {
		if _, err := a.CreateQuickReply(t.Context(), in); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := a.EditQuickReply(t.Context(), "x", QuickReplyInput{}); err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatal(err)
	}
	f.appStatePatchErr = errors.New("server rejected")
	if _, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("a"), Message: proto.String("x")}); !errors.Is(err, f.appStatePatchErr) {
		t.Fatal(err)
	}
	if pending, err := a.db.AppStateRecoveryRequired("regular"); err != nil || !pending {
		t.Fatalf("failed-send debt=%v %v", pending, err)
	}
}
