package store

import (
	"testing"
	"time"
)

func TestStatsCountsStoreRowsAndLastMessage(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	group := "456@g.us"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertChat(group, "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat group: %v", err)
	}
	if err := db.UpsertContact(chat, "123", "Alice", "", "", ""); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}
	if err := db.UpsertGroup(group, "Group", "owner@s.whatsapp.net", time.Now()); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}

	first := time.Date(2024, 4, 1, 12, 0, 0, 0, time.UTC)
	second := first.Add(5 * time.Minute)
	for _, row := range []UpsertMessageParams{
		{ChatJID: chat, MsgID: "m1", SenderJID: chat, Timestamp: first, Text: "one"},
		{ChatJID: chat, MsgID: "m2", SenderJID: chat, Timestamp: second, Text: "two"},
	} {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatalf("UpsertMessage %s: %v", row.MsgID, err)
		}
	}

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Messages != 2 || stats.Chats != 2 || stats.Contacts != 1 || stats.Groups != 1 {
		t.Fatalf("unexpected counts: %+v", stats)
	}
	if stats.LastMessageTS != second.Unix() {
		t.Fatalf("LastMessageTS = %d, want %d", stats.LastMessageTS, second.Unix())
	}
}

func TestStatsEmptyStore(t *testing.T) {
	db := openTestDB(t)

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats != (StoreStats{}) {
		t.Fatalf("expected zero stats, got %+v", stats)
	}
}
