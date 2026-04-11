package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestRefreshContactsStoresContacts(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	jid := types.JID{User: "111", Server: types.DefaultUserServer}
	f.contacts[jid] = types.ContactInfo{
		Found:     true,
		PushName:  "Push",
		FullName:  "Full Name",
		FirstName: "First",
	}

	if err := a.refreshContacts(context.Background()); err != nil {
		t.Fatalf("refreshContacts: %v", err)
	}
	c, err := a.db.GetContact(jid.String())
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.Name == "" {
		t.Fatalf("expected stored contact name, got empty")
	}
}

func TestRefreshGroupsStoresGroupsAndChats(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	gid := types.JID{User: "12345", Server: types.GroupServer}
	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f.groups[gid] = &types.GroupInfo{
		JID:          gid,
		OwnerJID:     types.JID{User: "999", Server: types.DefaultUserServer},
		GroupName:    types.GroupName{Name: "MyGroup"},
		GroupCreated: created,
	}

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("refreshGroups: %v", err)
	}
	gs, err := a.db.ListGroups("MyGroup", 10, false)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != gid.String() {
		t.Fatalf("expected group to be stored, got %+v", gs)
	}
	c, err := a.db.GetChat(gid.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Kind != "group" {
		t.Fatalf("expected chat kind group, got %q", c.Kind)
	}
}

func TestRefreshGroupsMarksMissingOlderGroupsLeft(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 55; i++ {
		gid := types.JID{User: fmt.Sprintf("%05d", i), Server: types.GroupServer}
		created := base.Add(time.Duration(i) * time.Minute)
		if err := a.db.UpsertGroup(gid.String(), fmt.Sprintf("Group %02d", i), "", created); err != nil {
			t.Fatalf("seed UpsertGroup(%d): %v", i, err)
		}
		if i != 0 {
			f.groups[gid] = &types.GroupInfo{
				JID:          gid,
				GroupName:    types.GroupName{Name: fmt.Sprintf("Group %02d", i)},
				GroupCreated: created,
			}
		}
	}

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("refreshGroups: %v", err)
	}

	active, err := a.db.ListGroups("", 0, false)
	if err != nil {
		t.Fatalf("ListGroups active: %v", err)
	}
	if len(active) != 54 {
		t.Fatalf("expected 54 active groups after marking one left, got %d", len(active))
	}

	all, err := a.db.ListGroups("", 0, true)
	if err != nil {
		t.Fatalf("ListGroups all: %v", err)
	}
	if len(all) != 55 {
		t.Fatalf("expected 55 total groups including left ones, got %d", len(all))
	}
	if all[len(all)-1].JID != "00000@g.us" {
		t.Fatalf("expected oldest group to remain queryable, got %q", all[len(all)-1].JID)
	}
}
