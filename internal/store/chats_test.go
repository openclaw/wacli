package store

import (
	"testing"
	"time"
)

func TestUpsertChatNameAndLastMessageTS(t *testing.T) {
	db := openTestDB(t)

	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	if err := db.UpsertChat("123@s.whatsapp.net", "dm", "Alice", t1); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	// Empty name should not clobber.
	if err := db.UpsertChat("123@s.whatsapp.net", "dm", "", t2); err != nil {
		t.Fatalf("UpsertChat empty name: %v", err)
	}
	c, err := db.GetChat("123@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Name != "Alice" {
		t.Fatalf("expected name to stay Alice, got %q", c.Name)
	}
	if !c.LastMessageTS.Equal(t2) {
		t.Fatalf("expected LastMessageTS=%s, got %s", t2, c.LastMessageTS)
	}

	// Older timestamp should not override.
	if err := db.UpsertChat("123@s.whatsapp.net", "dm", "Alice2", t1); err != nil {
		t.Fatalf("UpsertChat older ts: %v", err)
	}
	c, err = db.GetChat("123@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.LastMessageTS.Equal(t2) {
		t.Fatalf("expected LastMessageTS to remain %s, got %s", t2, c.LastMessageTS)
	}
}

func TestChatStateColumnsAndFilters(t *testing.T) {
	db := openTestDB(t)
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	for _, row := range []struct {
		jid  string
		name string
		ts   time.Time
	}{
		{"a@s.whatsapp.net", "Alice", now.Add(-2 * time.Hour)},
		{"b@g.us", "Team", now.Add(-1 * time.Hour)},
		{"c@s.whatsapp.net", "Carol", now},
	} {
		if err := db.UpsertChat(row.jid, "dm", row.name, row.ts); err != nil {
			t.Fatalf("UpsertChat %s: %v", row.jid, err)
		}
	}
	if err := db.SetChatArchived("a@s.whatsapp.net", true); err != nil {
		t.Fatalf("SetChatArchived: %v", err)
	}
	if err := db.SetChatPinned("c@s.whatsapp.net", true); err != nil {
		t.Fatalf("SetChatPinned: %v", err)
	}
	if err := db.SetChatMutedUntil("b@g.us", -1); err != nil {
		t.Fatalf("SetChatMutedUntil: %v", err)
	}
	if err := db.SetChatUnread("b@g.us", true); err != nil {
		t.Fatalf("SetChatUnread: %v", err)
	}
	if err := db.SetChatUnreadCount("b@g.us", 4); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}

	yes := true
	no := false
	cases := []struct {
		name   string
		filter ChatListFilter
		want   string
	}{
		{"archived", ChatListFilter{Archived: &yes}, "a@s.whatsapp.net"},
		{"not archived", ChatListFilter{Archived: &no}, "c@s.whatsapp.net"},
		{"pinned", ChatListFilter{Pinned: &yes}, "c@s.whatsapp.net"},
		{"muted", ChatListFilter{Muted: &yes}, "b@g.us"},
		{"unread", ChatListFilter{Unread: &yes}, "b@g.us"},
		{"not unread", ChatListFilter{Unread: &no}, "c@s.whatsapp.net"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chats, err := db.ListChatsFiltered(tc.filter)
			if err != nil {
				t.Fatalf("ListChatsFiltered: %v", err)
			}
			if len(chats) == 0 || chats[0].JID != tc.want {
				t.Fatalf("first chat = %+v, want %s", chats, tc.want)
			}
		})
	}

	chats, err := db.ListChats("", 10)
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 3 || chats[0].JID != "c@s.whatsapp.net" {
		t.Fatalf("expected pinned chat first, got %+v", chats)
	}
	if !chats[1].Muted() || !chats[1].Unread || chats[1].UnreadCount != 4 {
		t.Fatalf("expected muted/unread state on second chat, got %+v", chats[1])
	}
	if err := db.IncrementChatUnread("b@g.us"); err != nil {
		t.Fatalf("IncrementChatUnread: %v", err)
	}
	c, err := db.GetChat("b@g.us")
	if err != nil {
		t.Fatalf("GetChat incremented unread: %v", err)
	}
	if !c.Unread || c.UnreadCount != 5 {
		t.Fatalf("incremented unread = %+v, want count 5", c)
	}
	if err := db.SetChatUnread("b@g.us", true); err != nil {
		t.Fatalf("SetChatUnread true on counted chat: %v", err)
	}
	c, err = db.GetChat("b@g.us")
	if err != nil {
		t.Fatalf("GetChat preserved unread: %v", err)
	}
	if !c.Unread || c.UnreadCount != 5 {
		t.Fatalf("mark-unread changed counted unread = %+v, want count 5", c)
	}
	if err := db.SetChatUnread("b@g.us", false); err != nil {
		t.Fatalf("SetChatUnread false: %v", err)
	}
	c, err = db.GetChat("b@g.us")
	if err != nil {
		t.Fatalf("GetChat read: %v", err)
	}
	if c.Unread || c.UnreadCount != 0 {
		t.Fatalf("mark-read unread = %+v, want count 0", c)
	}
}

func TestMarkerOnlyUnreadHasNoUnreadCount(t *testing.T) {
	db := openTestDB(t)

	if err := db.UpsertChat("marker@s.whatsapp.net", "dm", "Marker", time.Now()); err != nil {
		t.Fatalf("UpsertChat marker: %v", err)
	}
	if err := db.UpsertChat("read@s.whatsapp.net", "dm", "Read", time.Now()); err != nil {
		t.Fatalf("UpsertChat read: %v", err)
	}
	if err := db.SetChatUnread("marker@s.whatsapp.net", true); err != nil {
		t.Fatalf("SetChatUnread marker: %v", err)
	}

	c, err := db.GetChat("marker@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat marker: %v", err)
	}
	if !c.Unread || c.UnreadCount != 0 {
		t.Fatalf("marker unread = %+v, want unread true count 0", c)
	}

	yes := true
	chats, err := db.ListChatsFiltered(ChatListFilter{Unread: &yes, Limit: 10})
	if err != nil {
		t.Fatalf("ListChatsFiltered unread: %v", err)
	}
	if len(chats) != 1 || chats[0].JID != "marker@s.whatsapp.net" || !chats[0].Unread || chats[0].UnreadCount != 0 {
		t.Fatalf("unread filter chats = %+v, want marker-only unread", chats)
	}

	no := false
	chats, err = db.ListChatsFiltered(ChatListFilter{Unread: &no, Limit: 10})
	if err != nil {
		t.Fatalf("ListChatsFiltered no-unread: %v", err)
	}
	if len(chats) != 1 || chats[0].JID != "read@s.whatsapp.net" {
		t.Fatalf("no-unread filter chats = %+v, want only read chat", chats)
	}

	if err := db.IncrementChatUnread("marker@s.whatsapp.net"); err != nil {
		t.Fatalf("IncrementChatUnread marker: %v", err)
	}
	c, err = db.GetChat("marker@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat incremented marker: %v", err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("incremented marker unread = %+v, want count 1", c)
	}
}

func TestChatStateSettersCreateMissingChat(t *testing.T) {
	db := openTestDB(t)

	if err := db.SetChatMutedUntil("missing@s.whatsapp.net", -1); err != nil {
		t.Fatalf("SetChatMutedUntil: %v", err)
	}
	c, err := db.GetChat("missing@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Kind != "unknown" || !c.Muted() {
		t.Fatalf("created chat = %+v", c)
	}
}
