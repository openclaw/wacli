package store

import (
	"testing"
	"time"
)

func TestDeleteChat(t *testing.T) {
	db := openTestDB(t)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat("123@s.whatsapp.net", "dm", "Alice", now); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   "123@s.whatsapp.net",
		MsgID:     "msg1",
		Timestamp: now,
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	msgCount, err := db.CountChatMessages("123@s.whatsapp.net")
	if err != nil {
		t.Fatalf("CountChatMessages: %v", err)
	}
	if msgCount != 1 {
		t.Fatalf("expected 1 message, got %d", msgCount)
	}

	if err := db.DeleteChat("123@s.whatsapp.net"); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}

	_, err = db.GetChat("123@s.whatsapp.net")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}

	msgCount, err = db.CountChatMessages("123@s.whatsapp.net")
	if err != nil {
		t.Fatalf("CountChatMessages after delete: %v", err)
	}
	if msgCount != 0 {
		t.Fatalf("expected 0 messages after delete, got %d", msgCount)
	}
}

func TestDeleteChatsOlderThan(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -200)
	recent := now.AddDate(0, 0, -30)

	if err := db.UpsertChat("old@s.whatsapp.net", "dm", "Old", old); err != nil {
		t.Fatalf("UpsertChat old: %v", err)
	}
	if err := db.UpsertChat("recent@s.whatsapp.net", "dm", "Recent", recent); err != nil {
		t.Fatalf("UpsertChat recent: %v", err)
	}

	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   "old@s.whatsapp.net",
		MsgID:     "msg1",
		Timestamp: old,
		Text:      "old message",
	}); err != nil {
		t.Fatalf("UpsertMessage old: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   "recent@s.whatsapp.net",
		MsgID:     "msg2",
		Timestamp: recent,
		Text:      "recent message",
	}); err != nil {
		t.Fatalf("UpsertMessage recent: %v", err)
	}

	deleted, err := db.DeleteChatsOlderThan(180)
	if err != nil {
		t.Fatalf("DeleteChatsOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	_, err = db.GetChat("old@s.whatsapp.net")
	if err == nil {
		t.Fatal("expected old chat to be deleted")
	}

	c, err := db.GetChat("recent@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat recent: %v", err)
	}
	if c.JID != "recent@s.whatsapp.net" {
		t.Fatalf("expected recent chat to survive, got %s", c.JID)
	}
}

func TestListChatsOlderThan(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -200)
	recent := now.AddDate(0, 0, -30)

	if err := db.UpsertChat("old@s.whatsapp.net", "dm", "Old", old); err != nil {
		t.Fatalf("UpsertChat old: %v", err)
	}
	if err := db.UpsertChat("recent@s.whatsapp.net", "dm", "Recent", recent); err != nil {
		t.Fatalf("UpsertChat recent: %v", err)
	}

	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   "old@s.whatsapp.net",
		MsgID:     "msg1",
		Timestamp: old,
		Text:      "old message",
	}); err != nil {
		t.Fatalf("UpsertMessage old: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   "recent@s.whatsapp.net",
		MsgID:     "msg2",
		Timestamp: recent,
		Text:      "recent message",
	}); err != nil {
		t.Fatalf("UpsertMessage recent: %v", err)
	}

	chats, err := db.ListChatsOlderThan(180)
	if err != nil {
		t.Fatalf("ListChatsOlderThan: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chats))
	}
	if chats[0].JID != "old@s.whatsapp.net" {
		t.Fatalf("expected old chat, got %s", chats[0].JID)
	}
}

func TestDeleteGroup(t *testing.T) {
	db := openTestDB(t)

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertGroup("12345@g.us", "Test Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}

	if err := db.DeleteGroup("12345@g.us"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	groups, err := db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups after delete, got %d", len(groups))
	}
}

func TestListLeftGroups(t *testing.T) {
	db := openTestDB(t)

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	leftAt := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := db.UpsertGroup("active@g.us", "Active Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup active: %v", err)
	}
	if err := db.UpsertGroup("left@g.us", "Left Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup left: %v", err)
	}

	if err := db.MarkGroupLeft("left@g.us", leftAt); err != nil {
		t.Fatalf("MarkGroupLeft: %v", err)
	}

	leftGroups, err := db.ListLeftGroups()
	if err != nil {
		t.Fatalf("ListLeftGroups: %v", err)
	}
	if len(leftGroups) != 1 {
		t.Fatalf("expected 1 left group, got %d", len(leftGroups))
	}
	if leftGroups[0].JID != "left@g.us" {
		t.Fatalf("expected left@g.us, got %s", leftGroups[0].JID)
	}
}

func TestDeleteLeftGroups(t *testing.T) {
	db := openTestDB(t)

	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	leftAt := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := db.UpsertGroup("active@g.us", "Active Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup active: %v", err)
	}
	if err := db.UpsertGroup("left@g.us", "Left Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup left: %v", err)
	}

	if err := db.MarkGroupLeft("left@g.us", leftAt); err != nil {
		t.Fatalf("MarkGroupLeft: %v", err)
	}

	deleted, err := db.DeleteLeftGroups()
	if err != nil {
		t.Fatalf("DeleteLeftGroups: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	groups, err := db.ListGroups("", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 active group remaining, got %d", len(groups))
	}
	if groups[0].JID != "active@g.us" {
		t.Fatalf("expected active@g.us, got %s", groups[0].JID)
	}
}

func TestDeleteLeftGroupsOlderThan(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	created := now.AddDate(0, 0, -365)
	oldLeft := now.AddDate(0, 0, -200)
	recentLeft := now.AddDate(0, 0, -30)

	if err := db.UpsertGroup("old-left@g.us", "Old Left", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup old-left: %v", err)
	}
	if err := db.UpsertGroup("recent-left@g.us", "Recent Left", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup recent-left: %v", err)
	}

	if err := db.MarkGroupLeft("old-left@g.us", oldLeft); err != nil {
		t.Fatalf("MarkGroupLeft old: %v", err)
	}
	if err := db.MarkGroupLeft("recent-left@g.us", recentLeft); err != nil {
		t.Fatalf("MarkGroupLeft recent: %v", err)
	}

	deleted, err := db.DeleteLeftGroupsOlderThan(180)
	if err != nil {
		t.Fatalf("DeleteLeftGroupsOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	leftGroups, err := db.ListLeftGroups()
	if err != nil {
		t.Fatalf("ListLeftGroups: %v", err)
	}
	if len(leftGroups) != 1 {
		t.Fatalf("expected 1 left group remaining, got %d", len(leftGroups))
	}
	if leftGroups[0].JID != "recent-left@g.us" {
		t.Fatalf("expected recent-left@g.us, got %s", leftGroups[0].JID)
	}
}
