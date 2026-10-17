package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func rawContactNames(t *testing.T, a *App, jid string) (fullName, firstName, phone string) {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var full, first, ph sql.NullString
	if err := raw.QueryRow(`SELECT full_name, first_name, phone FROM contacts WHERE jid = ?`, jid).Scan(&full, &first, &ph); err != nil {
		t.Fatalf("read contact %s: %v", jid, err)
	}
	return full.String, first.String, ph.String
}

// A mutation indexed by a LID the session cannot map is kept under the LID,
// as the whatsmeow contact store keeps it; a later `contacts resolve` or
// refresh can reconcile it. Mapped LIDs land on the phone-number row.
func TestContactEventKeysByMappedPhoneNumberOrLID(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	pn := types.NewJID("15550001111", types.DefaultUserServer)
	mapped := types.NewJID("987654321098765", types.HiddenUserServer)
	unmapped := types.NewJID("111222333444555", types.HiddenUserServer)
	f.lids[mapped] = pn
	for _, jid := range []types.JID{mapped, unmapped} {
		evt := &events.Contact{JID: jid, Action: &waSyncAction.ContactAction{FullName: proto.String("Ada Lovelace"), FirstName: proto.String("Ada")}}
		f.applyContactBatch(evt) // whatsmeow writes its store before it dispatches
		if err := a.handleContactEvent(context.Background(), evt); err != nil {
			t.Fatal(err)
		}
	}
	if full, first, phone := rawContactNames(t, a, pn.String()); full != "Ada Lovelace" || first != "Ada" || phone != pn.User {
		t.Fatalf("mapped LID row = %q/%q phone=%q", full, first, phone)
	}
	if _, err := a.db.GetContact(mapped.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mapped LID stored under the LID: %v", err)
	}
	if full, first, phone := rawContactNames(t, a, unmapped.String()); full != "Ada Lovelace" || first != "Ada" || phone != "" {
		t.Fatalf("unmapped LID row = %q/%q phone=%q", full, first, phone)
	}
}

func TestContactEventIgnoresNonUserAndMalformedEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550002222", types.DefaultUserServer)
	if err := a.db.UpsertContact(target.String(), target.User, "", "Grace Hopper", "Grace", ""); err != nil {
		t.Fatal(err)
	}
	for _, evt := range []*events.Contact{
		nil,
		{JID: target},
		{JID: types.NewJID("120363000000000000", types.GroupServer), Action: &waSyncAction.ContactAction{FullName: proto.String("Group")}},
		{JID: types.NewJID("15550004444", types.DefaultUserServer), Action: &waSyncAction.ContactAction{}},
	} {
		if err := a.handleContactEvent(context.Background(), evt); err != nil {
			t.Fatalf("handleContactEvent(%+v) = %v", evt, err)
		}
	}
	if full, first, _ := rawContactNames(t, a, target.String()); full != "Grace Hopper" || first != "Grace" {
		t.Fatalf("event without action changed names: %q/%q", full, first)
	}
	for _, jid := range []string{"120363000000000000@g.us", "15550004444@s.whatsapp.net"} {
		if _, err := a.db.GetContact(jid); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("contact row %s created: %v", jid, err)
		}
	}
}

// Contact echoes belong to critical_unblock_low, so a persistence failure
// leaves a recovery intent for that collection and nothing else.
func TestContactEventMapsToCriticalUnblockLow(t *testing.T) {
	got := appStateCollectionsForEvent(&events.Contact{})
	if len(got) != 1 || got[0] != appstate.WAPatchCriticalUnblockLow {
		t.Fatalf("collections = %v, want [%s]", got, appstate.WAPatchCriticalUnblockLow)
	}
}
