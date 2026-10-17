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
	pending := journal.Pending()
	if len(pending) != 1 || pending[0].Seq != 1 || pending[0].JID != contact || pending[0].FirstName != "Grace" || pending[0].FullName != "Grace Hopper" || pending[0].Err != nil {
		t.Fatalf("journal = %+v", pending)
	}
	if err := store.PutAllContactNames(ctx, []waStore.ContactEntry{{JID: contact, FirstName: "Ada", FullName: "Ada Lovelace"}}); err != nil {
		t.Fatal(err)
	}
	info, _ = c.client.Store.Contacts.GetContact(ctx, contact)
	pending = journal.Pending()
	if len(pending) != 2 || pending[1].Seq != 2 || pending[1].FirstName != "Ada" || pending[1].FullName != "Ada Lovelace" || info.FullName != "Ada Lovelace" {
		t.Fatalf("journal after PutAllContactNames = %+v, row = %+v", pending, info)
	}
	journal.Ack(1)
	if pending = journal.Pending(); len(pending) != 1 || pending[0].Seq != 2 {
		t.Fatalf("pending after Ack(1) = %+v", pending)
	}
	if journal.HighWater() != 2 {
		t.Fatalf("high water = %d", journal.HighWater())
	}
}

// The facts the mirror relies on, through the pinned SDK's dispatchAppState
// against a real sqlstore: the contact store keeps a phone-number row and the
// LID row it maps to independently; a nameless action stores empty names;
// a failed store write (cancelled context) leaves the old row, still emits
// the event with the new names, and is journaled with its error in order.
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
		failed      bool
	}{{pn, "Ada", "Ada Lovelace", false}, {lid, "Grace", "Grace Hopper", false}, {lid, "", "", false}, {pn, "Rear", "Rear Admiral Hopper", true}}
	pending := c.ContactJournal().Pending()
	if len(pending) != len(want) {
		t.Fatalf("journal = %+v", pending)
	}
	for i, w := range want {
		got := pending[i]
		if got.Seq != uint64(i+1) || got.JID != w.jid || got.FirstName != w.first || got.FullName != w.full || (got.Err != nil) != w.failed {
			t.Fatalf("journal[%d] = %+v, want %+v", i, got, w)
		}
	}
	if !errors.Is(pending[3].Err, context.Canceled) {
		t.Fatalf("failed write error = %v, want context.Canceled", pending[3].Err)
	}
}
