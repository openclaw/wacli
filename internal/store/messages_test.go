package store

import (
	"strings"
	"testing"
	"time"
)

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

func TestListMessagesFiltersAndOrdering(t *testing.T) {
	db := openTestDB(t)
	chat := "chat@s.whatsapp.net"
	otherChat := "other@s.whatsapp.net"
	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, jid := range []string{chat, otherChat} {
		if err := db.UpsertChat(jid, "dm", jid, base); err != nil {
			t.Fatalf("UpsertChat %s: %v", jid, err)
		}
	}
	rows := []UpsertMessageParams{
		{ChatJID: chat, MsgID: "old-from-alice", SenderJID: "alice@s.whatsapp.net", SenderName: "Alice", Timestamp: base, Text: "old"},
		{ChatJID: chat, MsgID: "new-from-me", SenderJID: "me@s.whatsapp.net", Timestamp: base.Add(time.Second), FromMe: true, Text: "new"},
		{ChatJID: chat, MsgID: "forwarded", SenderJID: "bob@s.whatsapp.net", Timestamp: base.Add(2 * time.Second), Text: "forwarded", IsForwarded: true, ForwardingScore: 2},
		{ChatJID: otherChat, MsgID: "other-chat", SenderJID: "alice@s.whatsapp.net", Timestamp: base.Add(3 * time.Second), Text: "other"},
	}
	for _, row := range rows {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatalf("UpsertMessage %s: %v", row.MsgID, err)
		}
	}
	starredAt := base.Add(4 * time.Second)
	if err := db.SetStarred(SetStarredParams{
		ChatJID:   chat,
		MsgID:     "new-from-me",
		SenderJID: "me@s.whatsapp.net",
		FromMe:    true,
		Starred:   true,
		StarredAt: starredAt,
	}); err != nil {
		t.Fatalf("SetStarred: %v", err)
	}

	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if got := messageIDs(msgs); got != "forwarded,new-from-me,old-from-alice" {
		t.Fatalf("default order = %s", got)
	}
	if msgs[2].SenderName != "Alice" {
		t.Fatalf("SenderName = %q, want Alice", msgs[2].SenderName)
	}

	msgs, err = db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10, Asc: true})
	if err != nil {
		t.Fatalf("ListMessages asc: %v", err)
	}
	if got := messageIDs(msgs); got != "old-from-alice,new-from-me,forwarded" {
		t.Fatalf("asc order = %s", got)
	}

	fromMe := true
	msgs, err = db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10, FromMe: &fromMe})
	if err != nil {
		t.Fatalf("ListMessages fromMe: %v", err)
	}
	if got := messageIDs(msgs); got != "new-from-me" {
		t.Fatalf("fromMe filter = %s", got)
	}

	msgs, err = db.ListMessages(ListMessagesParams{ChatJID: chat, SenderJID: "alice@s.whatsapp.net", Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages sender: %v", err)
	}
	if got := messageIDs(msgs); got != "old-from-alice" {
		t.Fatalf("sender filter = %s", got)
	}

	msgs, err = db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10, Forwarded: true})
	if err != nil {
		t.Fatalf("ListMessages forwarded: %v", err)
	}
	if got := messageIDs(msgs); got != "forwarded" {
		t.Fatalf("forwarded filter = %s", got)
	}
	if msgs[0].ForwardingScore != 2 {
		t.Fatalf("ForwardingScore = %d, want 2", msgs[0].ForwardingScore)
	}

	msgs, err = db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10, Starred: true})
	if err != nil {
		t.Fatalf("ListMessages starred: %v", err)
	}
	if got := messageIDs(msgs); got != "new-from-me" {
		t.Fatalf("starred filter = %s", got)
	}
	if !msgs[0].Starred || !msgs[0].StarredAt.Equal(starredAt) {
		t.Fatalf("unexpected starred metadata: %+v", msgs[0])
	}
}

func TestListMessagesFiltersMultipleChatJIDs(t *testing.T) {
	db := openTestDB(t)
	pn := "15551234567@s.whatsapp.net"
	lid := "123456789@lid"
	other := "other@s.whatsapp.net"
	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, jid := range []string{pn, lid, other} {
		if err := db.UpsertChat(jid, "dm", jid, base); err != nil {
			t.Fatalf("UpsertChat %s: %v", jid, err)
		}
	}
	rows := []UpsertMessageParams{
		{ChatJID: pn, MsgID: "pn-row", SenderJID: pn, Timestamp: base, Text: "phone"},
		{ChatJID: lid, MsgID: "lid-row", SenderJID: lid, Timestamp: base.Add(time.Second), Text: "hidden"},
		{ChatJID: other, MsgID: "other-row", SenderJID: other, Timestamp: base.Add(2 * time.Second), Text: "other"},
	}
	for _, row := range rows {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatalf("UpsertMessage %s: %v", row.MsgID, err)
		}
	}

	msgs, err := db.ListMessages(ListMessagesParams{ChatJIDs: []string{pn, lid}, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if got := messageIDs(msgs); got != "lid-row,pn-row" {
		t.Fatalf("ids = %s", got)
	}
}

func TestGetMessageReturnsRichDetails(t *testing.T) {
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	base := time.Date(2024, 3, 2, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID:       chat,
		ChatName:      "Alice",
		MsgID:         "mid",
		SenderJID:     chat,
		SenderName:    "Alice Example",
		Timestamp:     base,
		Text:          "raw caption",
		DisplayText:   "Sent image",
		ReactionToID:  "target-mid",
		ReactionEmoji: "👍",
		MediaType:     "image",
		MediaCaption:  "raw caption",
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
	downloadedAt := base.Add(time.Second)
	if err := db.MarkMediaDownloaded(chat, "mid", "/tmp/pic.jpg", downloadedAt); err != nil {
		t.Fatalf("MarkMediaDownloaded: %v", err)
	}
	starredAt := base.Add(2 * time.Second)
	if err := db.SetStarred(SetStarredParams{
		ChatJID:   chat,
		MsgID:     "mid",
		SenderJID: chat,
		Starred:   true,
		StarredAt: starredAt,
	}); err != nil {
		t.Fatalf("SetStarred: %v", err)
	}

	msg, err := db.GetMessage(chat, "mid")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.SenderName != "Alice Example" || msg.DisplayText != "Sent image" {
		t.Fatalf("unexpected text fields: %+v", msg)
	}
	if msg.ReactionToID != "target-mid" || msg.ReactionEmoji != "👍" {
		t.Fatalf("unexpected reaction fields: %+v", msg)
	}
	if msg.MediaCaption != "raw caption" || msg.Filename != "pic.jpg" || msg.MimeType != "image/jpeg" || msg.DirectPath != "/direct/path" {
		t.Fatalf("unexpected media fields: %+v", msg)
	}
	if msg.LocalPath != "/tmp/pic.jpg" || !msg.DownloadedAt.Equal(downloadedAt) {
		t.Fatalf("unexpected download fields: %+v", msg)
	}
	if !msg.Starred || !msg.StarredAt.Equal(starredAt) {
		t.Fatalf("unexpected starred fields: %+v", msg)
	}
}

func TestListStarredMessagesOrdersByStarredTime(t *testing.T) {
	db := openTestDB(t)
	chat := "starred@s.whatsapp.net"
	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Starred", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for _, row := range []UpsertMessageParams{
		{ChatJID: chat, MsgID: "m1", SenderJID: chat, Timestamp: base, Text: "first"},
		{ChatJID: chat, MsgID: "m2", SenderJID: chat, Timestamp: base.Add(time.Second), Text: "second"},
	} {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatalf("UpsertMessage %s: %v", row.MsgID, err)
		}
	}
	if err := db.SetStarred(SetStarredParams{ChatJID: chat, MsgID: "m1", Starred: true, StarredAt: base.Add(10 * time.Second)}); err != nil {
		t.Fatalf("SetStarred m1: %v", err)
	}
	if err := db.SetStarred(SetStarredParams{ChatJID: chat, MsgID: "m2", Starred: true, StarredAt: base.Add(5 * time.Second)}); err != nil {
		t.Fatalf("SetStarred m2: %v", err)
	}

	msgs, err := db.ListStarredMessages(ListStarredMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListStarredMessages: %v", err)
	}
	if got := messageIDs(msgs); got != "m1,m2" {
		t.Fatalf("starred order = %s", got)
	}

	if err := db.SetStarred(SetStarredParams{ChatJID: chat, MsgID: "m1", Starred: false}); err != nil {
		t.Fatalf("unstar m1: %v", err)
	}
	msgs, err = db.ListStarredMessages(ListStarredMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListStarredMessages after unstar: %v", err)
	}
	if got := messageIDs(msgs); got != "m2" {
		t.Fatalf("starred after unstar = %s", got)
	}
}

func TestListMessagesStableSameTimestampOrder(t *testing.T) {
	db := openTestDB(t)
	chat := "same-ts@s.whatsapp.net"
	ts := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Same TS", ts); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		if err := db.UpsertMessage(UpsertMessageParams{
			ChatJID:   chat,
			MsgID:     id,
			SenderJID: chat,
			Timestamp: ts,
			Text:      id,
		}); err != nil {
			t.Fatalf("UpsertMessage %s: %v", id, err)
		}
	}

	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages desc: %v", err)
	}
	if got := messageIDs(msgs); got != "m3,m2,m1" {
		t.Fatalf("desc order = %s", got)
	}

	msgs, err = db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10, Asc: true})
	if err != nil {
		t.Fatalf("ListMessages asc: %v", err)
	}
	if got := messageIDs(msgs); got != "m1,m2,m3" {
		t.Fatalf("asc order = %s", got)
	}

	ctx, err := db.MessageContext(chat, "m2", 1, 1)
	if err != nil {
		t.Fatalf("MessageContext: %v", err)
	}
	if got := messageIDs(ctx); got != "m1,m2,m3" {
		t.Fatalf("context order = %s", got)
	}
}

func messageIDs(msgs []Message) string {
	out := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, msg.MsgID)
	}
	return strings.Join(out, ",")
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
