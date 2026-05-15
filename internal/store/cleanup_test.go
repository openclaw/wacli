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
	if err := db.UpsertPoll(Poll{
		ChatJID:   "123@s.whatsapp.net",
		MsgID:     "poll1",
		Question:  "Q?",
		Options:   []string{"yes", "no"},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertPoll: %v", err)
	}
	if err := db.UpsertPollVote(PollVote{
		ChatJID:   "123@s.whatsapp.net",
		PollMsgID: "poll1",
		VoterJID:  "voter@s.whatsapp.net",
		VoteMsgID: "vote1",
		Selected:  []string{"yes"},
		VotedAt:   now,
	}); err != nil {
		t.Fatalf("UpsertPollVote: %v", err)
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
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM polls WHERE chat_jid = ?", "123@s.whatsapp.net"); got != 0 {
		t.Fatalf("expected polls deleted, got %d", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM poll_votes WHERE chat_jid = ?", "123@s.whatsapp.net"); got != 0 {
		t.Fatalf("expected poll votes deleted, got %d", got)
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
	if err := db.UpsertPoll(Poll{
		ChatJID:   "old@s.whatsapp.net",
		MsgID:     "old-poll",
		Question:  "Old?",
		Options:   []string{"yes", "no"},
		CreatedAt: old,
	}); err != nil {
		t.Fatalf("UpsertPoll old: %v", err)
	}
	if err := db.UpsertPollVote(PollVote{
		ChatJID:   "old@s.whatsapp.net",
		PollMsgID: "old-poll",
		VoterJID:  "voter@s.whatsapp.net",
		VoteMsgID: "old-vote",
		Selected:  []string{"yes"},
		VotedAt:   old,
	}); err != nil {
		t.Fatalf("UpsertPollVote old: %v", err)
	}
	if err := db.UpsertPoll(Poll{
		ChatJID:   "recent@s.whatsapp.net",
		MsgID:     "recent-poll",
		Question:  "Recent?",
		Options:   []string{"yes", "no"},
		CreatedAt: recent,
	}); err != nil {
		t.Fatalf("UpsertPoll recent: %v", err)
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
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM polls WHERE chat_jid = ?", "old@s.whatsapp.net"); got != 0 {
		t.Fatalf("expected old polls deleted, got %d", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM poll_votes WHERE chat_jid = ?", "old@s.whatsapp.net"); got != 0 {
		t.Fatalf("expected old poll votes deleted, got %d", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM polls WHERE chat_jid = ?", "recent@s.whatsapp.net"); got != 1 {
		t.Fatalf("expected recent poll to survive, got %d", got)
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

func TestListChatsOlderThanSkipsUnknownActivity(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpsertChat("unknown@s.whatsapp.net", "dm", "Unknown", time.Time{}); err != nil {
		t.Fatalf("UpsertChat unknown: %v", err)
	}

	chats, err := db.ListChatsOlderThan(1)
	if err != nil {
		t.Fatalf("ListChatsOlderThan: %v", err)
	}
	if len(chats) != 0 {
		t.Fatalf("expected unknown-activity chat to be skipped, got %#v", chats)
	}

	deleted, err := db.DeleteChatsOlderThan(1)
	if err != nil {
		t.Fatalf("DeleteChatsOlderThan: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deleted, got %d", deleted)
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

func TestDeleteGroupLocalDataDeletesGroupChatAndMessages(t *testing.T) {
	db := openTestDB(t)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertGroup("12345@g.us", "Test Group", "owner@s.whatsapp.net", now); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.UpsertChat("12345@g.us", "group", "Test Group", now); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   "12345@g.us",
		MsgID:     "msg1",
		Timestamp: now,
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	if err := db.UpsertPoll(Poll{
		ChatJID:   "12345@g.us",
		MsgID:     "poll1",
		Question:  "Lunch?",
		Options:   []string{"yes", "no"},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertPoll: %v", err)
	}
	if err := db.UpsertPollVote(PollVote{
		ChatJID:   "12345@g.us",
		PollMsgID: "poll1",
		VoterJID:  "voter@s.whatsapp.net",
		VoteMsgID: "vote1",
		Selected:  []string{"yes"},
		VotedAt:   now,
	}); err != nil {
		t.Fatalf("UpsertPollVote: %v", err)
	}

	if err := db.DeleteGroupLocalData("12345@g.us"); err != nil {
		t.Fatalf("DeleteGroupLocalData: %v", err)
	}
	if groups, err := db.ListGroups("", 10); err != nil {
		t.Fatalf("ListGroups: %v", err)
	} else if len(groups) != 0 {
		t.Fatalf("expected group deleted, got %#v", groups)
	}
	if _, err := db.GetChat("12345@g.us"); err == nil {
		t.Fatalf("expected chat to be deleted")
	}
	if got := countRows(t, db.sql, `SELECT COUNT(1) FROM messages WHERE chat_jid = ?`, "12345@g.us"); got != 0 {
		t.Fatalf("expected messages deleted, got %d", got)
	}
	if got := countRows(t, db.sql, `SELECT COUNT(1) FROM polls WHERE chat_jid = ?`, "12345@g.us"); got != 0 {
		t.Fatalf("expected polls deleted, got %d", got)
	}
	if got := countRows(t, db.sql, `SELECT COUNT(1) FROM poll_votes WHERE chat_jid = ?`, "12345@g.us"); got != 0 {
		t.Fatalf("expected poll votes deleted, got %d", got)
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

func TestListPrunableGroupsHonorsAgeAndIncludeActive(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	created := now.AddDate(0, 0, -400)
	oldLeft := now.AddDate(0, 0, -200)
	recentLeft := now.AddDate(0, 0, -30)
	oldActive := now.AddDate(0, 0, -220)
	recentActive := now.AddDate(0, 0, -10)

	for _, tc := range []struct {
		jid  string
		name string
	}{
		{"old-left@g.us", "Old Left"},
		{"recent-left@g.us", "Recent Left"},
		{"old-active@g.us", "Old Active"},
		{"recent-active@g.us", "Recent Active"},
		{"unknown-active@g.us", "Unknown Active"},
	} {
		if err := db.UpsertGroup(tc.jid, tc.name, "owner@s.whatsapp.net", created); err != nil {
			t.Fatalf("UpsertGroup %s: %v", tc.jid, err)
		}
	}
	if err := db.MarkGroupLeft("old-left@g.us", oldLeft); err != nil {
		t.Fatalf("MarkGroupLeft old: %v", err)
	}
	if err := db.MarkGroupLeft("recent-left@g.us", recentLeft); err != nil {
		t.Fatalf("MarkGroupLeft recent: %v", err)
	}
	if err := db.UpsertChat("old-active@g.us", "group", "Old Active", oldActive); err != nil {
		t.Fatalf("UpsertChat old active: %v", err)
	}
	if err := db.UpsertChat("recent-active@g.us", "group", "Recent Active", recentActive); err != nil {
		t.Fatalf("UpsertChat recent active: %v", err)
	}

	leftOnly, err := db.ListPrunableGroups(180, false)
	if err != nil {
		t.Fatalf("ListPrunableGroups left only: %v", err)
	}
	if got, want := groupJIDs(leftOnly), []string{"old-left@g.us"}; !sameStrings(got, want) {
		t.Fatalf("left-only targets = %#v, want %#v", got, want)
	}

	withActive, err := db.ListPrunableGroups(180, true)
	if err != nil {
		t.Fatalf("ListPrunableGroups include active: %v", err)
	}
	if got, want := groupJIDs(withActive), []string{"old-left@g.us", "old-active@g.us"}; !sameStrings(got, want) {
		t.Fatalf("include-active targets = %#v, want %#v", got, want)
	}

	allLeft, err := db.ListPrunableGroups(0, false)
	if err != nil {
		t.Fatalf("ListPrunableGroups all left: %v", err)
	}
	if got, want := groupJIDs(allLeft), []string{"old-left@g.us", "recent-left@g.us"}; !sameStrings(got, want) {
		t.Fatalf("all-left targets = %#v, want %#v", got, want)
	}
}

func groupJIDs(groups []Group) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.JID)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
