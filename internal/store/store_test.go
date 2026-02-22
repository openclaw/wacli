package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countRows(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	row := db.QueryRow(q, args...)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatalf("countRows scan: %v", err)
	}
	return n
}

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

func TestMessageUpsertIdempotentAndContext(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	msgs := []struct {
		id   string
		ts   time.Time
		text string
	}{
		{"m1", base.Add(1 * time.Second), "first"},
		{"m2", base.Add(2 * time.Second), "second"},
		{"m3", base.Add(3 * time.Second), "third"},
	}
	for _, m := range msgs {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID:    chat,
			ChatName:   "Alice",
			MsgID:      m.id,
			SenderJID:  chat,
			SenderName: "Alice",
			Timestamp:  m.ts,
			FromMe:     false,
			Text:       m.text,
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", m.id, err)
		}
	}

	// Upsert same message again should not create duplicates.
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:    chat,
		ChatName:   "Alice",
		MsgID:      "m2",
		SenderJID:  chat,
		SenderName: "Alice",
		Timestamp:  base.Add(2 * time.Second),
		FromMe:     false,
		Text:       "second",
	}); err != nil {
		t.Fatalf("UpsertMessage again: %v", err)
	}
	if got := countRows(t, db.sql, "SELECT COUNT(*) FROM messages WHERE chat_jid = ?", chat); got != 3 {
		t.Fatalf("expected 3 messages, got %d", got)
	}

	ctx, err := db.MessageContext(chat, "m2", 1, 1)
	if err != nil {
		t.Fatalf("MessageContext: %v", err)
	}
	if len(ctx) != 3 {
		t.Fatalf("expected 3 context messages, got %d", len(ctx))
	}
	if ctx[0].MsgID != "m1" || ctx[1].MsgID != "m2" || ctx[2].MsgID != "m3" {
		t.Fatalf("unexpected context order: %v, %v, %v", ctx[0].MsgID, ctx[1].MsgID, ctx[2].MsgID)
	}
}

func TestMessageQueriesReturnSenderName(t *testing.T) {
	db := openTestDB(t)

	chat := "group@g.us"
	if err := db.UpsertChat(chat, "group", "Team", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"m1", "m2", "m3"} {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID:    chat,
			ChatName:   "Team",
			MsgID:      id,
			SenderJID:  "sender@s.whatsapp.net",
			SenderName: "Alice",
			Timestamp:  base.Add(time.Duration(i+1) * time.Second),
			Text:       "hello",
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", id, err)
		}
	}

	listed, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected 3 listed messages, got %d", len(listed))
	}
	for _, m := range listed {
		if m.SenderName != "Alice" {
			t.Fatalf("expected sender name Alice in list, got %q", m.SenderName)
		}
	}

	got, err := db.GetMessage(chat, "m2")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.SenderName != "Alice" {
		t.Fatalf("expected sender name Alice in GetMessage, got %q", got.SenderName)
	}

	ctx, err := db.MessageContext(chat, "m2", 1, 1)
	if err != nil {
		t.Fatalf("MessageContext: %v", err)
	}
	if len(ctx) != 3 {
		t.Fatalf("expected 3 context messages, got %d", len(ctx))
	}
	for _, m := range ctx {
		if m.SenderName != "Alice" {
			t.Fatalf("expected sender name Alice in context, got %q", m.SenderName)
		}
	}
}

func TestMediaDownloadInfoAndMarkDownloaded(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	ts := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:       chat,
		ChatName:      "Alice",
		MsgID:         "mid",
		SenderJID:     chat,
		SenderName:    "Alice",
		Timestamp:     ts,
		FromMe:        false,
		Text:          "",
		MediaType:     "image",
		MediaCaption:  "cap",
		Filename:      "pic.jpg",
		MimeType:      "image/jpeg",
		DirectPath:    "/direct/path",
		MediaKey:      []byte{1, 2, 3},
		FileSHA256:    []byte{4, 5},
		FileEncSHA256: []byte{6, 7},
		FileLength:    123,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	info, err := db.GetMediaDownloadInfo(chat, "mid")
	if err != nil {
		t.Fatalf("GetMediaDownloadInfo: %v", err)
	}
	if info.MediaType != "image" || info.MimeType != "image/jpeg" || info.DirectPath != "/direct/path" {
		t.Fatalf("unexpected media info: %+v", info)
	}
	if info.FileLength != 123 {
		t.Fatalf("expected FileLength=123, got %d", info.FileLength)
	}

	when := time.Date(2024, 3, 1, 0, 0, 1, 0, time.UTC)
	if err := db.MarkMediaDownloaded(chat, "mid", "/tmp/file", when); err != nil {
		t.Fatalf("MarkMediaDownloaded: %v", err)
	}
	info, err = db.GetMediaDownloadInfo(chat, "mid")
	if err != nil {
		t.Fatalf("GetMediaDownloadInfo: %v", err)
	}
	if info.LocalPath != "/tmp/file" {
		t.Fatalf("expected LocalPath set, got %q", info.LocalPath)
	}
	if !info.DownloadedAt.Equal(when) {
		t.Fatalf("expected DownloadedAt=%s, got %s", when, info.DownloadedAt)
	}
}

func TestContactsAliasTagsAndSearch(t *testing.T) {
	db := openTestDB(t)

	jid := "111@s.whatsapp.net"
	if err := db.UpsertContact(jid, "111", "Push", "Full Name", "First", "Biz"); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}
	if err := db.SetAlias(jid, "Ali"); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}
	if err := db.AddTag(jid, "friends"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if err := db.AddTag(jid, "work"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}

	c, err := db.GetContact(jid)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.Alias != "Ali" {
		t.Fatalf("expected alias Ali, got %q", c.Alias)
	}
	if len(c.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %v", c.Tags)
	}

	found, err := db.SearchContacts("Ali", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(found) != 1 || found[0].JID != jid {
		t.Fatalf("expected to find contact by alias, got %+v", found)
	}

	if err := db.RemoveTag(jid, "work"); err != nil {
		t.Fatalf("RemoveTag: %v", err)
	}
	if err := db.RemoveAlias(jid); err != nil {
		t.Fatalf("RemoveAlias: %v", err)
	}
	c, err = db.GetContact(jid)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.Alias != "" {
		t.Fatalf("expected alias removed, got %q", c.Alias)
	}
	if len(c.Tags) != 1 || c.Tags[0] != "friends" {
		t.Fatalf("expected remaining tag friends, got %v", c.Tags)
	}
}

func TestCountMessagesAndOldestMessageInfo(t *testing.T) {
	db := openTestDB(t)

	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	if n, err := db.CountMessages(); err != nil || n != 0 {
		t.Fatalf("CountMessages expected 0, got %d (err=%v)", n, err)
	}

	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	_ = db.UpsertMessage(UpsertMessageParams{
		ChatJID:    chat,
		MsgID:      "m2",
		Timestamp:  base.Add(2 * time.Second),
		FromMe:     true,
		SenderJID:  chat,
		SenderName: "Alice",
		Text:       "second",
	})
	_ = db.UpsertMessage(UpsertMessageParams{
		ChatJID:    chat,
		MsgID:      "m1",
		Timestamp:  base.Add(1 * time.Second),
		FromMe:     false,
		SenderJID:  chat,
		SenderName: "Alice",
		Text:       "first",
	})

	oldest, err := db.GetOldestMessageInfo(chat)
	if err != nil {
		t.Fatalf("GetOldestMessageInfo: %v", err)
	}
	if oldest.MsgID != "m1" {
		t.Fatalf("expected oldest m1, got %q", oldest.MsgID)
	}
	if !oldest.Timestamp.Equal(base.Add(1 * time.Second)) {
		t.Fatalf("unexpected oldest timestamp: %s", oldest.Timestamp)
	}
	if oldest.FromMe {
		t.Fatalf("expected oldest.FromMe=false")
	}

	if n, err := db.CountMessages(); err != nil || n != 2 {
		t.Fatalf("CountMessages expected 2, got %d (err=%v)", n, err)
	}
}

func TestGroupsUpsertListAndParticipantsReplace(t *testing.T) {
	db := openTestDB(t)

	gid := "123@g.us"
	created := time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertGroup(gid, "Group", "owner@s.whatsapp.net", created); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}
	if err := db.ReplaceGroupParticipants(gid, []GroupParticipant{
		{GroupJID: gid, UserJID: "a@s.whatsapp.net", Role: "admin"},
		{GroupJID: gid, UserJID: "b@s.whatsapp.net", Role: ""},
	}); err != nil {
		t.Fatalf("ReplaceGroupParticipants: %v", err)
	}

	gs, err := db.ListGroups("Gro", 10)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(gs) != 1 || gs[0].JID != gid {
		t.Fatalf("expected group in list, got %+v", gs)
	}

	admins := countRows(t, db.sql, "SELECT COUNT(*) FROM group_participants WHERE group_jid=? AND role='admin'", gid)
	members := countRows(t, db.sql, "SELECT COUNT(*) FROM group_participants WHERE group_jid=? AND role='member'", gid)
	if admins != 1 || members != 1 {
		t.Fatalf("expected roles admin=1 member=1, got admin=%d member=%d", admins, members)
	}
}
