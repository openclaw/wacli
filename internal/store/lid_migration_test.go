package store

import (
	"reflect"
	"testing"
	"time"
)

func TestHistoricalLIDJIDsFindsChatAndMessageColumns(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	group := "120363000000@g.us"
	for _, jid := range []string{pn, lid, group} {
		if err := db.UpsertChat(jid, "dm", jid, base); err != nil {
			t.Fatalf("UpsertChat %s: %v", jid, err)
		}
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   lid,
		MsgID:     "lid-chat",
		SenderJID: lid,
		Timestamp: base,
		Text:      "lid chat",
	}); err != nil {
		t.Fatalf("UpsertMessage lid chat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   group,
		MsgID:     "group-sender",
		SenderJID: lid,
		Timestamp: base,
		Text:      "group sender",
	}); err != nil {
		t.Fatalf("UpsertMessage group sender: %v", err)
	}

	got, err := db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs: %v", err)
	}
	if want := []string{lid}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HistoricalLIDJIDs = %#v, want %#v", got, want)
	}
}

func TestMigrateLIDToPNMergesChatsAndMessages(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	group := "120363000000@g.us"
	if err := db.UpsertChat(pn, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat pn: %v", err)
	}
	if err := db.UpsertChat(lid, "unknown", lid, base.Add(10*time.Second)); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}
	if err := db.UpsertChat(group, "group", "Project", base); err != nil {
		t.Fatalf("UpsertChat group: %v", err)
	}

	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   pn,
		MsgID:     "dupe",
		SenderJID: "",
		Timestamp: base,
	}); err != nil {
		t.Fatalf("UpsertMessage pn dupe: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    lid,
		ChatName:   "Alice LID",
		MsgID:      "dupe",
		SenderJID:  lid,
		SenderName: "Alice",
		Timestamp:  base.Add(5 * time.Second),
		Text:       "from lid",
	}); err != nil {
		t.Fatalf("UpsertMessage lid dupe: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    lid,
		ChatName:   "Alice LID",
		MsgID:      "lid-only",
		SenderJID:  lid,
		SenderName: "Alice",
		Timestamp:  base.Add(6 * time.Second),
		Text:       "only on lid",
	}); err != nil {
		t.Fatalf("UpsertMessage lid only: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:   group,
		MsgID:     "group",
		SenderJID: lid,
		Timestamp: base.Add(7 * time.Second),
		Text:      "group message",
	}); err != nil {
		t.Fatalf("UpsertMessage group: %v", err)
	}

	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}
	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN idempotent: %v", err)
	}

	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM chats WHERE jid = ?", lid); got != 0 {
		t.Fatalf("lid chat rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE chat_jid = ?", lid); got != 0 {
		t.Fatalf("lid chat message rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE sender_jid = ?", lid); got != 0 {
		t.Fatalf("lid sender rows = %d, want 0", got)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE chat_jid = ?", pn); got != 2 {
		t.Fatalf("pn message rows = %d, want 2", got)
	}

	chat, err := db.GetChat(pn)
	if err != nil {
		t.Fatalf("GetChat pn: %v", err)
	}
	if chat.Name != "Alice" {
		t.Fatalf("merged chat name = %q, want Alice", chat.Name)
	}
	if !chat.LastMessageTS.Equal(base.Add(10 * time.Second)) {
		t.Fatalf("merged chat timestamp = %s, want %s", chat.LastMessageTS, base.Add(10*time.Second))
	}

	dupe, err := db.GetMessage(pn, "dupe")
	if err != nil {
		t.Fatalf("GetMessage dupe: %v", err)
	}
	if dupe.Text != "from lid" {
		t.Fatalf("merged duplicate text = %q, want from lid", dupe.Text)
	}
	if dupe.SenderJID != pn {
		t.Fatalf("merged duplicate sender = %q, want %q", dupe.SenderJID, pn)
	}
	if !dupe.Timestamp.Equal(base.Add(5 * time.Second)) {
		t.Fatalf("merged duplicate timestamp = %s, want %s", dupe.Timestamp, base.Add(5*time.Second))
	}

	groupMsg, err := db.GetMessage(group, "group")
	if err != nil {
		t.Fatalf("GetMessage group: %v", err)
	}
	if groupMsg.SenderJID != pn {
		t.Fatalf("group sender = %q, want %q", groupMsg.SenderJID, pn)
	}

	lids, err := db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs after migrate: %v", err)
	}
	if len(lids) != 0 {
		t.Fatalf("HistoricalLIDJIDs after migrate = %#v, want none", lids)
	}
}
