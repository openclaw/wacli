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
	ownerLID := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	ownerPN := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[ownerLID] = ownerPN
	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	f.groups[gid] = &types.GroupInfo{
		JID:               gid,
		OwnerJID:          ownerLID,
		GroupName:         types.GroupName{Name: "MyGroup"},
		GroupCreated:      created,
		GroupLinkedParent: types.GroupLinkedParent{LinkedParentJID: types.JID{User: "parent", Server: types.GroupServer}},
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
		t.Fatalf("expected linked parent to be stored, got %+v", gs[0])
	}
	if gs[0].OwnerJID != ownerPN.String() {
		t.Fatalf("OwnerJID = %q, want %q", gs[0].OwnerJID, ownerPN.String())
	}
	c, err := a.db.GetChat(gid.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Kind != "group" {
		t.Fatalf("expected chat kind group, got %q", c.Kind)
	}
}

func TestRefreshGroupsPreservesChatLastMessageTimestamp(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	gid := types.JID{User: "12345", Server: types.GroupServer}
	messageTS := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	refreshTS := messageTS.Add(24 * time.Hour)
	if err := a.db.UpsertChat(gid.String(), "group", "Old Name", messageTS); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	f.groups[gid] = &types.GroupInfo{
		JID:       gid,
		GroupName: types.GroupName{Name: "New Name"},
	}
	previousNowUTC := nowUTC
	nowUTC = func() time.Time { return refreshTS }
	t.Cleanup(func() { nowUTC = previousNowUTC })

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("refreshGroups: %v", err)
	}
	c, err := a.db.GetChat(gid.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Name != "New Name" {
		t.Fatalf("expected refreshed chat name, got %q", c.Name)
	}
	if !c.LastMessageTS.Equal(messageTS) {
		t.Fatalf("expected LastMessageTS=%s, got %s", messageTS, c.LastMessageTS)
	}
}

func TestRefreshGroupsReplacesParticipantSnapshot(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	gid := types.JID{User: "12345", Server: types.GroupServer}
	first := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	f.groups[gid] = &types.GroupInfo{
		JID: gid,
		Participants: []types.GroupParticipant{
			{JID: first},
		},
	}

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("first refreshGroups: %v", err)
	}
	participants, err := a.db.ListGroupParticipants(gid.String())
	if err != nil {
		t.Fatalf("ListGroupParticipants after first refresh: %v", err)
	}
	if len(participants) != 1 || participants[0].UserJID != first.String() {
		t.Fatalf("first participant snapshot = %+v, want %s", participants, first)
	}

	adminLID := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	adminPN := types.JID{User: "15550000002", Server: types.DefaultUserServer}
	member := types.JID{User: "15550000003", Server: types.DefaultUserServer}
	f.lids[adminLID] = adminPN
	f.groups[gid].Participants = []types.GroupParticipant{
		{JID: adminLID, IsAdmin: true},
		{JID: member},
	}

	if err := a.refreshGroups(context.Background()); err != nil {
		t.Fatalf("second refreshGroups: %v", err)
	}
	participants, err = a.db.ListGroupParticipants(gid.String())
	if err != nil {
		t.Fatalf("ListGroupParticipants after second refresh: %v", err)
	}
	if len(participants) != 2 {
		t.Fatalf("second participant snapshot = %+v, want 2 participants", participants)
	}
	if participants[0].UserJID != adminPN.String() || participants[0].Role != "admin" {
		t.Fatalf("first refreshed participant = %+v, want resolved admin %s", participants[0], adminPN)
	}
	if participants[1].UserJID != member.String() || participants[1].Role != "member" {
		t.Fatalf("second refreshed participant = %+v, want member %s", participants[1], member)
	}
}

func TestRefreshNewslettersStoresChats(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	jid := types.JID{User: "12345", Server: types.NewsletterServer}
	f.news[jid] = &types.NewsletterMetadata{
		ID: jid,
		ThreadMeta: types.NewsletterThreadMetadata{
			Name: types.NewsletterText{Text: "Launch Notes"},
		},
	}

	if err := a.refreshNewsletters(context.Background()); err != nil {
		t.Fatalf("refreshNewsletters: %v", err)
	}
	c, err := a.db.GetChat(jid.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Kind != "newsletter" || c.Name != "Launch Notes" {
		t.Fatalf("expected newsletter chat, got %+v", c)
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
