package app

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func newLabelWriteApp(t *testing.T, seed ...any) (*App, *fakeWA) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	if err := a.persistFetchedAppStateEvents(t.Context(), seed, &appStatePersistenceTracker{}); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func onlyPatch(t *testing.T, f *fakeWA) appstate.MutationInfo {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appStatePatches) != 1 {
		t.Fatalf("patches = %d, want 1", len(f.appStatePatches))
	}
	p := f.appStatePatches[0]
	if p.Type != appstate.WAPatchRegular || len(p.Mutations) != 1 || p.Timestamp.IsZero() {
		t.Fatalf("patch = %+v, want one timestamped regular mutation", p)
	}
	return p.Mutations[0]
}

func noPatch(t *testing.T, f *fakeWA) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appStatePatches) != 0 {
		t.Fatalf("unexpected patch sent: %+v", f.appStatePatches)
	}
}

func labelEvent(id, name string, at time.Time, full bool, mutate func(*waSyncAction.LabelEditAction)) *events.LabelEdit {
	act := &waSyncAction.LabelEditAction{Name: proto.String(name), Color: proto.Int32(1)}
	if mutate != nil {
		mutate(act)
	}
	return &events.LabelEdit{LabelID: id, Timestamp: at, FromFullSync: full, Action: act}
}

func TestCreateLabelUsesNextIDAfterPreWriteSync(t *testing.T) {
	at := time.Unix(100, 0)
	a, f := newLabelWriteApp(t,
		labelEvent("3", "Old", at, true, nil),
		labelEvent("5", "Gone", at, true, func(l *waSyncAction.LabelEditAction) { l.Deleted = proto.Bool(true) }),
	)
	// A label created on the phone arrives with the pre-write fetch; the new
	// ID must be computed after it is persisted, not from a stale index.
	f.appStateFetchEvent = func(name string, _, _ bool) any {
		if name != string(appstate.WAPatchRegular) {
			return nil
		}
		return labelEvent("9", "From phone", at.Add(time.Second), false, nil)
	}
	created, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba"), Color: proto.Int32(4)})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "10" || created.Name != "Prueba" || *created.Color != 4 {
		t.Fatalf("created = %+v", created)
	}
	m := onlyPatch(t, f)
	act := m.Value.GetLabelEditAction()
	if !reflect.DeepEqual(m.Index, []string{appstate.IndexLabelEdit, "10"}) || m.Version != 3 ||
		act.GetName() != "Prueba" || act.GetColor() != 4 || act.GetDeleted() || act.Type != nil {
		t.Fatalf("mutation = %v v%d %v", m.Index, m.Version, act)
	}
	if fetches := f.appStateFetches; len(fetches) == 0 || fetches[0].name != string(appstate.WAPatchRegular) {
		t.Fatalf("no pre-write regular sync: %+v", fetches)
	}
	got, err := a.db.ResolveListForWrite("Prueba")
	if err != nil || got.ID != "10" {
		t.Fatalf("local index = %+v, %v", got, err)
	}
}

func TestCreateLabelRefusesGuessingIDWithoutFullSync(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("3", "Old", time.Unix(100, 0), false, nil))
	_, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba")})
	if err == nil || !strings.Contains(err.Error(), "pass --id") {
		t.Fatalf("err = %v, want explicit ID request", err)
	}
	noPatch(t, f)
	// An explicit unused ID is accepted; a live one is not.
	if _, err := a.CreateLabel(t.Context(), LabelInput{ID: "3", Name: proto.String("Prueba")}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("reused ID: %v", err)
	}
	if _, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Old"), ID: "40"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate name: %v", err)
	}
	noPatch(t, f)
	created, err := a.CreateLabel(t.Context(), LabelInput{ID: "40", Name: proto.String("Prueba")})
	if err != nil || created.ID != "40" || *created.Color != 0 {
		t.Fatalf("explicit ID: %+v, %v", created, err)
	}
}

func TestCreateLabelFollowsCustomListModel(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("3", "Custom", time.Unix(100, 0), true, func(l *waSyncAction.LabelEditAction) {
		l.Type = waSyncAction.LabelEditAction_CUSTOM.Enum()
	}))
	if _, err := a.CreateLabel(t.Context(), LabelInput{Name: proto.String("Prueba")}); err != nil {
		t.Fatal(err)
	}
	act := onlyPatch(t, f).Value.GetLabelEditAction()
	if act.GetType() != waSyncAction.LabelEditAction_CUSTOM || !act.GetIsActive() {
		t.Fatalf("new list on a Lists account = %v", act)
	}
}

func TestEditAndDeleteLabelResendStoredFields(t *testing.T) {
	at := time.Unix(100, 0)
	a, f := newLabelWriteApp(t, labelEvent("2", "New customer", at, true, func(l *waSyncAction.LabelEditAction) {
		l.PredefinedID, l.OrderIndex, l.IsActive = proto.Int32(1), proto.Int32(7), proto.Bool(true)
		l.Type = waSyncAction.LabelEditAction_PREDEFINED.Enum()
	}))
	edited, err := a.EditLabel(t.Context(), "New customer", LabelInput{Name: proto.String("Nuevo")})
	if err != nil {
		t.Fatal(err)
	}
	act := onlyPatch(t, f).Value.GetLabelEditAction()
	if act.GetName() != "Nuevo" || act.GetColor() != 1 || act.GetPredefinedID() != 1 || act.GetOrderIndex() != 7 ||
		!act.GetIsActive() || act.GetType() != waSyncAction.LabelEditAction_PREDEFINED || act.GetDeleted() {
		t.Fatalf("edit dropped stored fields: %v", act)
	}
	if edited.Name != "Nuevo" {
		t.Fatalf("edited = %+v", edited)
	}
	f.appStatePatches = nil
	if _, err := a.DeleteLabel(t.Context(), "2"); err != nil {
		t.Fatal(err)
	}
	act = onlyPatch(t, f).Value.GetLabelEditAction()
	if !act.GetDeleted() || act.GetName() != "Nuevo" || act.GetPredefinedID() != 1 {
		t.Fatalf("delete = %v", act)
	}
	if _, err := a.db.ResolveListForWrite("2"); err == nil {
		t.Fatal("deleted label still resolves")
	}
}

func TestLabelWritesRejectImmutableAndSystemLists(t *testing.T) {
	at := time.Unix(100, 0)
	a, f := newLabelWriteApp(t,
		labelEvent("1", "Locked", at, true, func(l *waSyncAction.LabelEditAction) { l.IsImmutable = proto.Bool(true) }),
		labelEvent("2", "Unread", at, true, func(l *waSyncAction.LabelEditAction) { l.Type = waSyncAction.LabelEditAction_UNREAD.Enum() }),
	)
	if _, err := a.EditLabel(t.Context(), "1", LabelInput{Name: proto.String("x")}); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable edit: %v", err)
	}
	if _, err := a.DeleteLabel(t.Context(), "2"); err == nil || !strings.Contains(err.Error(), "system type") {
		t.Fatalf("system delete: %v", err)
	}
	if _, err := a.SetChatLabel(t.Context(), types.NewJID("123", types.DefaultUserServer), "2", true); err == nil {
		t.Fatal("system list accepted a chat")
	}
	if _, err := a.EditLabel(t.Context(), "1", LabelInput{Color: proto.Int32(20)}); err == nil || !strings.Contains(err.Error(), "between 0 and 19") {
		t.Fatalf("bad color: %v", err)
	}
	noPatch(t, f)
}

func TestSetChatLabelSendsPhoneJIDAndUpdatesFilter(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("7", "Ventas", time.Unix(100, 0), true, nil))
	lid := types.NewJID("999", types.HiddenUserServer)
	pn := types.NewJID("123", types.DefaultUserServer)
	f.lids[lid] = pn
	result, err := a.SetChatLabel(t.Context(), lid, "Ventas", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Chat != pn.String() || result.Label.ID != "7" {
		t.Fatalf("result = %+v, want the patched phone JID", result)
	}
	m := onlyPatch(t, f)
	if !reflect.DeepEqual(m.Index, []string{appstate.IndexLabelAssociationChat, "7", pn.String()}) || !m.Value.GetLabelAssociationAction().GetLabeled() {
		t.Fatalf("mutation = %v %v", m.Index, m.Value)
	}
	assertAppListMembers(t, a, pn.String())
	f.appStatePatches = nil
	if _, err := a.SetChatLabel(t.Context(), pn, "7", false); err != nil {
		t.Fatal(err)
	}
	if onlyPatch(t, f).Value.GetLabelAssociationAction().GetLabeled() {
		t.Fatal("remove sent labeled=true")
	}
	assertAppListMembers(t, a)
	f.appStatePatches = nil
	if _, err := a.SetChatLabel(t.Context(), types.NewJID("555", types.HiddenUserServer), "7", true); err == nil || !strings.Contains(err.Error(), "LID-only") {
		t.Fatalf("unmapped LID: %v", err)
	}
	noPatch(t, f)
}

func TestLabelWriteKeepsRecoveryIntentOnSendError(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("7", "Ventas", time.Unix(100, 0), true, nil))
	f.appStatePatchErr = errors.New("server rejected patch")
	if _, err := a.EditLabel(t.Context(), "7", LabelInput{Name: proto.String("Otro")}); !errors.Is(err, f.appStatePatchErr) {
		t.Fatalf("err = %v", err)
	}
	if l, err := a.db.ResolveListForWrite("7"); err != nil || l.Name != "Ventas" {
		t.Fatalf("failed write changed the local index: %+v, %v", l, err)
	}
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegular))
	if err != nil || !required {
		t.Fatalf("recovery intent after ambiguous send = %v, %v", required, err)
	}
}

func quickReplyEvent(id, shortcut, message string, at time.Time, deleted bool) *events.AppState {
	return &events.AppState{Index: []string{appstate.IndexQuickReply, id}, SyncActionValue: &waSyncAction.SyncActionValue{
		Timestamp: proto.Int64(at.UnixMilli()),
		QuickReplyAction: &waSyncAction.QuickReplyAction{Shortcut: proto.String(shortcut), Message: proto.String(message),
			Keywords: []string{"k"}, Count: proto.Int32(3), Deleted: proto.Bool(deleted), AssociatedLabelIDs: []string{"7"}},
	}}
}

func TestQuickReplyEventsPersistFromSyncAndOneShot(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		a := newTestApp(t)
		f := newFakeWA()
		a.wa = f
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
		evt := quickReplyEvent("1700000000", "hola", "Hola, gracias", time.Unix(100, 0), false)
		if !reflect.DeepEqual(appStateCollectionsForEvent(evt), []appstate.WAPatchName{appstate.WAPatchRegular}) {
			t.Fatal("quick reply event not routed to the regular collection")
		}
		f.emit(evt)
		waitQuickReplies(t, a, 1)
		got, _ := a.db.QuickReplies()
		q := got.QuickReplies[0]
		if q.Shortcut != "hola" || q.Message != "Hola, gracias" || *q.Count != 3 || !reflect.DeepEqual(q.LabelIDs, []string{"7"}) {
			t.Fatalf("stored = %+v", q)
		}
		f.emit(quickReplyEvent("1700000000", "hola", "", time.Unix(101, 0), true))
		waitQuickReplies(t, a, 0)
		// A replay of the current tombstone can have a lower device clock.
		f.emit(quickReplyEvent("1700000000", "hola", "", time.Unix(99, 0), true))
		time.Sleep(50 * time.Millisecond)
		waitQuickReplies(t, a, 0)
		// A regular full sync can contain only quick replies (or no labels).
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
		deadline := time.Now().Add(5 * time.Second)
		for {
			result, err := a.db.QuickReplies()
			if err != nil {
				t.Fatal(err)
			}
			if result.Completeness == "complete" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("full-sync completeness = %q", result.Completeness)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func waitQuickReplies(t *testing.T, a *App, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := a.db.QuickReplies()
		if err != nil {
			t.Fatal(err)
		}
		if len(got.QuickReplies) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("quick replies = %+v, want %d", got.QuickReplies, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestQuickReplyWritesBuildRegularPatches(t *testing.T) {
	a, f := newLabelWriteApp(t)
	at := time.Unix(1_700_000_000, 0).UTC()
	defer func(prev func() time.Time) { nowUTC = prev }(nowUTC)
	nowUTC = func() time.Time { return at }
	created, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("prueba"), Message: proto.String("Mensaje de prueba"), Keywords: &[]string{"test"}})
	if err != nil {
		t.Fatal(err)
	}
	m := onlyPatch(t, f)
	act := m.Value.GetQuickReplyAction()
	if !reflect.DeepEqual(m.Index, []string{appstate.IndexQuickReply, "1700000000"}) || m.Version != 3 ||
		act.GetShortcut() != "prueba" || act.GetMessage() != "Mensaje de prueba" || act.GetDeleted() || !reflect.DeepEqual(act.GetKeywords(), []string{"test"}) {
		t.Fatalf("create mutation = %v v%d %v", m.Index, m.Version, act)
	}
	if created.ID != "1700000000" {
		t.Fatalf("created = %+v", created)
	}
	f.appStatePatches = nil
	if _, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("/prueba"), Message: proto.String("x")}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate shortcut: %v", err)
	}
	noPatch(t, f)
	// A second reply in the same second gets the next free ID.
	if q, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("otra"), Message: proto.String("y")}); err != nil || q.ID != "1700000001" {
		t.Fatalf("same-second create = %+v, %v", q, err)
	}
	f.appStatePatches = nil
	nowUTC = func() time.Time { return at.Add(time.Minute) }
	if _, err := a.EditQuickReply(t.Context(), "/prueba", QuickReplyInput{Message: proto.String("Nuevo texto")}); err != nil {
		t.Fatal(err)
	}
	act = onlyPatch(t, f).Value.GetQuickReplyAction()
	if act.GetShortcut() != "prueba" || act.GetMessage() != "Nuevo texto" || !reflect.DeepEqual(act.GetKeywords(), []string{"test"}) {
		t.Fatalf("edit dropped stored fields: %v", act)
	}
	f.appStatePatches = nil
	nowUTC = func() time.Time { return at.Add(2 * time.Minute) }
	if _, err := a.DeleteQuickReply(t.Context(), "1700000000"); err != nil {
		t.Fatal(err)
	}
	if act = onlyPatch(t, f).Value.GetQuickReplyAction(); !act.GetDeleted() || act.GetMessage() != "Nuevo texto" {
		t.Fatalf("delete = %v", act)
	}
	got, err := a.db.QuickReplies()
	if err != nil || len(got.QuickReplies) != 1 || got.QuickReplies[0].Shortcut != "otra" {
		t.Fatalf("local index after delete = %+v, %v", got, err)
	}
}

func TestExplicitLabelIDRejectsTombstonesAndNonNumericIDs(t *testing.T) {
	for _, id := range []string{"7", "abc", "7x", "-1", "+1", "1.5", "01"} {
		t.Run(id, func(t *testing.T) {
			a, f := newLabelWriteApp(t, labelEvent("7", "Gone", time.Unix(100, 0), true, func(l *waSyncAction.LabelEditAction) { l.Deleted = proto.Bool(true) }))
			if _, err := a.CreateLabel(t.Context(), LabelInput{ID: id, Name: proto.String("New")}); err == nil {
				t.Fatalf("accepted unsafe ID %q", id)
			}
			noPatch(t, f)
		})
	}
}

func TestEditLabelRejectsDuplicateName(t *testing.T) {
	a, f := newLabelWriteApp(t, labelEvent("1", "First", time.Unix(100, 0), true, nil), labelEvent("2", "Second", time.Unix(100, 0), true, nil))
	if _, err := a.EditLabel(t.Context(), "1", LabelInput{Name: proto.String("Second")}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate rename: %v", err)
	}
	noPatch(t, f)
	if _, err := a.EditLabel(t.Context(), "1", LabelInput{Name: proto.String("First")}); err != nil {
		t.Fatalf("unchanged name: %v", err)
	}
}

func TestLabelRewritePreservesAbsentColor(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(strconv.FormatBool(deleted), func(t *testing.T) {
			a, f := newLabelWriteApp(t, labelEvent("1", "First", time.Unix(100, 0), true, func(l *waSyncAction.LabelEditAction) { l.Color = nil }))
			var err error
			if deleted {
				_, err = a.DeleteLabel(t.Context(), "1")
			} else {
				_, err = a.EditLabel(t.Context(), "1", LabelInput{Name: proto.String("New")})
			}
			if err != nil {
				t.Fatal(err)
			}
			if act := onlyPatch(t, f).Value.GetLabelEditAction(); act.Color != nil {
				t.Fatalf("absent color became %v", act.Color)
			}
		})
	}
}

func TestQuickReplyDuplicateCheckRejectsAmbiguityAndDatabaseErrors(t *testing.T) {
	for _, brokenDB := range []bool{false, true} {
		t.Run(strconv.FormatBool(brokenDB), func(t *testing.T) {
			a, f := newLabelWriteApp(t, quickReplyEvent("1", "dup", "one", time.Unix(100, 0), false), quickReplyEvent("2", "/dup", "two", time.Unix(100, 0), false), quickReplyEvent("3", "other", "three", time.Unix(100, 0), false))
			if brokenDB {
				raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec(`UPDATE quick_replies SET keywords='bad json' WHERE id='1'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.CreateQuickReply(t.Context(), QuickReplyInput{Shortcut: proto.String("dup"), Message: proto.String("x")}); err == nil {
				t.Fatal("create ignored shortcut resolution error")
			}
			noPatch(t, f)
			if _, err := a.EditQuickReply(t.Context(), "3", QuickReplyInput{Shortcut: proto.String("dup")}); err == nil {
				t.Fatal("rename ignored shortcut resolution error")
			}
			noPatch(t, f)
		})
	}
}

func TestQuickReplyInputValidation(t *testing.T) {
	a, f := newLabelWriteApp(t)
	for _, in := range []QuickReplyInput{
		{Shortcut: proto.String("/"), Message: proto.String("x")},
		{Shortcut: proto.String(" a"), Message: proto.String("x")},
		{Shortcut: proto.String("a"), Message: proto.String(" ")},
		{Shortcut: proto.String("a"), Message: proto.String("x"), Keywords: &[]string{""}},
		{Shortcut: proto.String("a")},
	} {
		if _, err := a.CreateQuickReply(t.Context(), in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
	if _, err := a.EditQuickReply(t.Context(), "a", QuickReplyInput{}); err == nil {
		t.Fatal("empty edit accepted")
	}
	noPatch(t, f)
}

func TestQuickReplySkewedClockWriteReplayAndFollowingEdit(t *testing.T) {
	at := time.Unix(1700000000, 0)
	a, f := newLabelWriteApp(t, quickReplyEvent("1", "hello", "old text", at.Add(time.Hour), false))
	defer func(prev func() time.Time) { nowUTC = prev }(nowUTC)
	nowUTC = func() time.Time { return at }
	if _, err := a.EditQuickReply(t.Context(), "1", QuickReplyInput{Message: proto.String("accepted text")}); err != nil {
		t.Fatal(err)
	}
	got, _, err := a.db.QuickReplyByID("1")
	if err != nil || got.Message != "accepted text" {
		t.Fatalf("accepted edit = %+v, %v", got, err)
	}
	// The echo/full replay belongs after the accepted edit, despite its lower clock.
	f.appStateFetchEvent = func(string, bool, bool) any { return quickReplyEvent("1", "hello", "accepted text", at, false) }
	f.appStatePatches = nil
	if _, err := a.EditQuickReply(t.Context(), "1", QuickReplyInput{Keywords: &[]string{"next"}}); err != nil {
		t.Fatal(err)
	}
	if act := onlyPatch(t, f).Value.GetQuickReplyAction(); act.GetMessage() != "accepted text" {
		t.Fatalf("following edit resurrected text: %v", act)
	}
	// A later phone patch is authoritative even if its timestamp regresses again.
	if err := a.persistFetchedAppStateEvents(t.Context(), []any{quickReplyEvent("1", "hello", "later phone patch", at.Add(-time.Minute), false)}, &appStatePersistenceTracker{}); err != nil {
		t.Fatal(err)
	}
	got, _, err = a.db.QuickReplyByID("1")
	if err != nil || got.Message != "later phone patch" {
		t.Fatalf("later patch = %+v, %v", got, err)
	}
}

func TestLabelSkewedClockWritesFollowPersistenceOrder(t *testing.T) {
	at := time.Unix(1700000000, 0)
	a, f := newLabelWriteApp(t, labelEvent("7", "Old", at.Add(time.Hour), true, nil))
	defer func(prev func() time.Time) { nowUTC = prev }(nowUTC)
	nowUTC = func() time.Time { return at }
	if _, err := a.EditLabel(t.Context(), "7", LabelInput{Name: proto.String("Accepted")}); err != nil {
		t.Fatal(err)
	}
	got, _, err := a.db.ListByID("7")
	if err != nil || got.Name != "Accepted" {
		t.Fatalf("accepted label = %+v, %v", got, err)
	}
	chat := types.NewJID("123", types.DefaultUserServer)
	if err := a.db.SetChatList(chat.String(), "7", true, at.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	f.appStatePatches = nil
	if _, err := a.SetChatLabel(t.Context(), chat, "7", false); err != nil {
		t.Fatal(err)
	}
	assertAppListMembers(t, a)
}

func TestEditMutedListPreservesMuteUntil(t *testing.T) {
	const until int64 = 1900000000123
	a, f := newLabelWriteApp(t, labelEvent("7", "Muted", time.Unix(100, 0), true, func(act *waSyncAction.LabelEditAction) {
		act.Type = waSyncAction.LabelEditAction_CUSTOM.Enum()
		act.MuteEndTimeMS = proto.Int64(until)
	}))
	if _, err := a.EditLabel(t.Context(), "7", LabelInput{Name: proto.String("Renamed")}); err != nil {
		t.Fatal(err)
	}
	if act := onlyPatch(t, f).Value.GetLabelEditAction(); act.MuteEndTimeMS == nil || act.GetMuteEndTimeMS() != until {
		t.Fatalf("muted list edit lost mute: %v", act)
	}
}

func TestQuickReplySnapshotDoesNotOvertakeLowerClockLivePatch(t *testing.T) {
	a, _ := newLabelWriteApp(t)
	at := time.Unix(100, 0)
	ticket := a.appStatePersist.reserve()
	a.handleAppStatePersistenceEvent(t.Context(), quickReplyEvent("1", "hello", "new live text", at.Add(-time.Second), false), nil)
	frontier := a.appStatePersist.complete(ticket, func() {
		if err := a.persistFetchedAppStateEvents(t.Context(), []any{quickReplyEvent("1", "hello", "older snapshot text", at, false)}, &appStatePersistenceTracker{}); err != nil {
			t.Error(err)
		}
	})
	if err := a.appStatePersist.waitThrough(t.Context(), frontier); err != nil {
		t.Fatal(err)
	}
	got, _, err := a.db.QuickReplyByID("1")
	if err != nil || got.Message != "new live text" {
		t.Fatalf("snapshot overtook live patch: %+v, %v", got, err)
	}
}
