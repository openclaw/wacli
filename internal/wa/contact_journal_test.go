package wa

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// The store.ContactStore interface declares PutContactName(ctx, user,
// fullName, firstName), but the sqlstore implementation and whatsmeow's
// caller both use (firstName, fullName). The journal must record what the
// store actually stores. Pinned against the real sqlstore, called through the
// interface the way dispatchAppState calls it.
func TestContactStoreJournalRecordsTheSQLStoreArgumentOrder(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	journal := c.ContactJournal()
	if journal == nil {
		t.Fatal("no contact journal installed on the open session")
	}
	var store waStore.ContactStore = c.client.Store.Contacts
	if store != journal {
		t.Fatalf("whatsmeow's contact store is %T, want the journal", store)
	}
	ctx := t.Context()
	contact := types.NewJID("15550002222", types.DefaultUserServer)
	if err := store.PutContactName(ctx, contact, "Grace", "Grace Hopper"); err != nil {
		t.Fatal(err)
	}
	info, err := c.client.Store.Contacts.GetContact(ctx, contact)
	if err != nil || !info.Found || info.FirstName != "Grace" || info.FullName != "Grace Hopper" {
		t.Fatalf("sqlstore row = %+v, %v; want first Grace, full Grace Hopper", info, err)
	}
	pending, _ := journal.Snapshot()
	if len(pending) != 1 || pending[0].Seq != 1 || pending[0].JID != contact || pending[0].FirstName != "Grace" || pending[0].FullName != "Grace Hopper" {
		t.Fatalf("journal = %+v", pending)
	}
	if err := store.PutAllContactNames(ctx, []waStore.ContactEntry{{JID: contact, FirstName: "Ada", FullName: "Ada Lovelace"}}); err != nil {
		t.Fatal(err)
	}
	info, _ = c.client.Store.Contacts.GetContact(ctx, contact)
	pending, _ = journal.Snapshot()
	if len(pending) != 2 || pending[1].Seq != 2 || pending[1].FirstName != "Ada" || pending[1].FullName != "Ada Lovelace" || info.FullName != "Ada Lovelace" {
		t.Fatalf("journal after PutAllContactNames = %+v, row = %+v", pending, info)
	}
	journal.Ack(1)
	pending, highWater := journal.Snapshot()
	if len(pending) != 1 || pending[0].Seq != 2 {
		t.Fatalf("pending after Ack(1) = %+v", pending)
	}
	if highWater != 2 {
		t.Fatalf("high water = %d", highWater)
	}
}

// The facts the mirror relies on, through the pinned SDK's dispatchAppState
// against a real sqlstore: the contact store keeps a phone-number row and the
// LID row it maps to independently; a nameless action stores empty names;
// a failed store write (cancelled context) leaves the old row, still emits
// the event with the new names, and is journaled in order.
func TestSDKContactStoreWritesAreJournaledInApplicationOrder(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := t.Context()
	pn := types.NewJID("15550002222", types.DefaultUserServer)
	lid := types.NewJID("222222222222222", types.HiddenUserServer)
	if err := c.client.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	apply := func(ctx context.Context, jid types.JID, full, first string) *events.Contact {
		t.Helper()
		evt := c.client.DangerousInternals().DispatchAppState(ctx, appstate.WAPatchCriticalUnblockLow, appstate.Mutation{
			Operation: waServerSync.SyncdMutation_SET,
			Index:     []string{appstate.IndexContact, jid.String()},
			Action:    &waSyncAction.SyncActionValue{ContactAction: &waSyncAction.ContactAction{FullName: proto.String(full), FirstName: proto.String(first)}},
		}, false)
		contact, ok := evt.(*events.Contact)
		if !ok {
			t.Fatalf("dispatchAppState returned %T, want *events.Contact", evt)
		}
		return contact
	}
	apply(ctx, pn, "Ada Lovelace", "Ada")
	apply(ctx, lid, "Grace Hopper", "Grace")
	apply(ctx, lid, "", "")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	evt := apply(cancelled, pn, "Rear Admiral Hopper", "Rear")

	all, err := c.GetAllContacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all[pn].FullName != "Ada Lovelace" || !all[lid].Found || all[lid].FullName != "" || all[lid].FirstName != "" {
		t.Fatalf("session rows: PN=%+v LID=%+v; want independent rows, LID cleared, PN untouched by the failed write", all[pn], all[lid])
	}
	if evt.Action.GetFullName() != "Rear Admiral Hopper" {
		t.Fatalf("event after the failed store write = %+v", evt.Action)
	}
	want := []struct {
		jid         types.JID
		first, full string
	}{{pn, "Ada", "Ada Lovelace"}, {lid, "Grace", "Grace Hopper"}, {lid, "", ""}, {pn, "Rear", "Rear Admiral Hopper"}}
	pending, _ := c.ContactJournal().Snapshot()
	if len(pending) != len(want) {
		t.Fatalf("journal = %+v", pending)
	}
	for i, w := range want {
		got := pending[i]
		if got.Seq != uint64(i+1) || got.JID != w.jid || got.FirstName != w.first || got.FullName != w.full {
			t.Fatalf("journal[%d] = %+v, want %+v", i, got, w)
		}
	}
}

type failingAppStateStore struct {
	waStore.AppStateStore
	err error
}

func (s failingAppStateStore) PutAppStateVersion(context.Context, string, uint64, [128]byte) error {
	return s.err
}

// The open session's app-state store is wrapped so the journal sees the
// collection cursor writes; a failed write is not counted. Coverage decides
// from the cursor writes and the own name's position in the same sequence.
func TestContactStoreJournalCoverage(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	journal := c.ContactJournal()
	ctx := t.Context()
	name := string(appstate.WAPatchCriticalUnblockLow)
	own := types.NewJID("15550001111", types.DefaultUserServer)
	other := types.NewJID("15550002222", types.DefaultUserServer)
	cursor := func(version uint64) {
		t.Helper()
		if err := c.client.Store.AppState.PutAppStateVersion(ctx, name, version, [128]byte{}); err != nil {
			t.Fatal(err)
		}
	}
	write := func(jid types.JID) {
		t.Helper()
		if err := c.client.Store.Contacts.PutContactName(ctx, jid, "First", "Full Name"); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name        string
		steps       func()
		covered     bool
		ownWritten  bool
		wantCursors uint64
	}{
		{"nothing consumed", func() {}, true, false, 0},
		{"own patch applied", func() { cursor(81); write(own) }, true, true, 1},
		{"one patch without the own name", func() { cursor(81); write(other) }, false, false, 1},
		{"own name written before the only move", func() { write(own); cursor(81) }, false, true, 1},
		{"two patches", func() { cursor(81); cursor(82); write(other); write(own) }, false, true, 2},
		{"a deleted version counts", func() {
			if err := c.client.Store.AppState.DeleteAppStateVersion(ctx, name); err != nil {
				t.Fatal(err)
			}
			cursor(81)
			write(own)
		}, false, true, 2},
		{"another collection does not count", func() {
			if err := c.client.Store.AppState.PutAppStateVersion(ctx, string(appstate.WAPatchRegularLow), 9, [128]byte{}); err != nil {
				t.Fatal(err)
			}
			cursor(81)
			write(own)
		}, true, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := journal.Mark(name)
			tc.steps()
			covered, ownWritten, end := journal.Coverage(name, own, start)
			if covered != tc.covered || ownWritten != tc.ownWritten || end.CursorWrites-start.CursorWrites != tc.wantCursors {
				t.Fatalf("Coverage = covered %v, own written %v, cursor writes %d; want %v, %v, %d",
					covered, ownWritten, end.CursorWrites-start.CursorWrites, tc.covered, tc.ownWritten, tc.wantCursors)
			}
		})
	}
	t.Run("a failed cursor write is not counted", func(t *testing.T) {
		failing := journal.AppStateStore(failingAppStateStore{AppStateStore: c.client.Store.AppState, err: errors.New("disk full")})
		start := journal.Mark(name)
		if err := failing.PutAppStateVersion(ctx, name, 99, [128]byte{}); err == nil {
			t.Fatal("test setup: write did not fail")
		}
		if end := journal.Mark(name); end != start {
			t.Fatalf("mark moved from %+v to %+v on a failed write", start, end)
		}
	})
}
