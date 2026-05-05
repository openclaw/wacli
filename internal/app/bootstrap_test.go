package app

import (
	"context"
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
		GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: types.JID{
			User:   "parent",
			Server: types.GroupServer,
		}},
	}

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("refreshGroups: %v", err)
	}
	gs, err := a.db.ListGroups("MyGroup", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != gid.String() {
		t.Fatalf("expected group to be stored, got %+v", gs)
	}
	if gs[0].LinkedParentJID != "parent@g.us" {
		t.Fatalf("LinkedParentJID = %q, want parent@g.us", gs[0].LinkedParentJID)
	}
	c, err := a.db.GetChat(gid.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Kind != "group" {
		t.Fatalf("expected chat kind group, got %q", c.Kind)
	}
}

func TestRefreshGroupsMarksMissingGroupsLeft(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	active := types.JID{User: "active", Server: types.GroupServer}
	left := types.JID{User: "left", Server: types.GroupServer}
	if err := a.db.UpsertGroup(active.String(), "Active", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup active: %v", err)
	}
	if err := a.db.UpsertGroup(left.String(), "Left", "", time.Time{}); err != nil {
		t.Fatalf("UpsertGroup left: %v", err)
	}
	f.groups[active] = &types.GroupInfo{
		JID:       active,
		GroupName: types.GroupName{Name: "Active"},
	}

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("refreshGroups: %v", err)
	}
	gs, err := a.db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != active.String() {
		t.Fatalf("expected only active group, got %+v", gs)
	}
}
