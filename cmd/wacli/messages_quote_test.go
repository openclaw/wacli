package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func TestMessageLineShowsMediaCaption(t *testing.T) {
	for _, c := range []struct {
		m    store.Message
		want string
	}{
		{store.Message{MediaType: "image", DisplayText: "Sent image", Text: "dinner receipt, 5,27 each", MediaCaption: "dinner receipt, 5,27 each"}, "Sent image: dinner receipt, 5,27 each"},
		{store.Message{MediaType: "document", DisplayText: "Sent document", Filename: "invoice-0042.pdf"}, "Sent document: invoice-0042.pdf"},
		{store.Message{MediaType: "document", DisplayText: "Sent document", MediaCaption: "the October one", Filename: "invoice-0042.pdf"}, "Sent document: the October one · invoice-0042.pdf"},
		{store.Message{MediaType: "live_location", DisplayText: "Sent live location", MediaCaption: "on my way"}, "Sent live location: on my way"},
		{store.Message{MediaType: "audio", DisplayText: "Sent audio", Text: "[Audio]", MediaCaption: "[Audio]"}, "Sent audio"},
		{store.Message{MediaType: "image", DisplayText: "Sent image"}, "Sent image"},
		{store.Message{MediaType: "image", DisplayText: "new caption", Text: "new caption", MediaCaption: "old caption"}, "new caption"},
		{store.Message{MediaType: "image", Text: "cap", MediaCaption: "cap", Revoked: true}, store.DeletedMessageDisplayText},
		// Not filled by withQuotes: the line sync stored, the caption after it.
		{store.Message{QuotedMsgID: "q", MediaType: "image", DisplayText: "> hello\nSent image", Text: "and this one", MediaCaption: "and this one"}, "> hello\nSent image: and this one"},
	} {
		if got := messageLine(c.m); got != c.want {
			t.Errorf("messageLine(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestReplyPartsSplitsAMultilineQuote(t *testing.T) {
	quote, own, ok := replyParts(store.Message{QuotedMsgID: "q", DisplayText: "> line 1\nline 2\nok", Text: "ok"})
	if !ok || quote != "line 1\nline 2" || own != "ok" {
		t.Fatalf("replyParts = %q, %q, %v", quote, own, ok)
	}
	if _, _, ok := replyParts(store.Message{DisplayText: "> not a reply\nhi", Text: "> not a reply\nhi"}); ok {
		t.Fatal("a message without a quoted id is no reply")
	}
}

func TestWithQuotesNamesTheAuthorAndCarriesTheCaption(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	group, ana, ben := "120363000000000001@g.us", "15550000001@s.whatsapp.net", "15550000002@s.whatsapp.net"
	now := time.Date(2026, 11, 15, 13, 45, 0, 0, time.UTC)
	if err := db.UpsertChat(group, "group", "Dinner", now); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertContact(ben, "15550000002", "Ben", "", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.UpsertMessageParams{
		{ChatJID: group, MsgID: "photo", SenderJID: ana, SenderName: "Ana", Timestamp: now, Text: "dinner receipt, 5,27 each", DisplayText: "Sent image",
			MediaType: "image", MediaCaption: "dinner receipt, 5,27 each"},
		{ChatJID: group, MsgID: "mine", FromMe: true, Timestamp: now.Add(time.Minute), Text: "paid", DisplayText: "> Sent image\npaid", QuotedMsgID: "photo", QuotedSenderJID: ana},
		{ChatJID: group, MsgID: "reply", SenderJID: ana, SenderName: "Ana", Timestamp: now.Add(2 * time.Minute), Text: "thanks", DisplayText: "> paid\nthanks",
			QuotedMsgID: "mine", QuotedSenderJID: "15550000009@s.whatsapp.net"},
		{ChatJID: group, MsgID: "gone", SenderJID: ana, Timestamp: now.Add(3 * time.Minute), Text: "and you?", DisplayText: "> on Friday\nand you?", QuotedMsgID: "old", QuotedSenderJID: ben},
	} {
		if err := db.UpsertMessage(p); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.ListMessagesPage("list", store.ListMessagesParams{ChatJIDs: []string{group}, Limit: 10, Asc: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	msgs := withQuotes(context.Background(), nil, db, page.Messages)
	got := map[string]store.Message{}
	for _, m := range msgs {
		got[m.MsgID] = m
	}
	for id, want := range map[string]string{
		"photo": "Sent image: dinner receipt, 5,27 each",
		"mine":  "> Ana: «Sent image: dinner receipt, 5,27 each» paid",
		"reply": "> me: «paid» thanks",
		"gone":  "> Ben: «on Friday» and you?",
	} {
		if line := messageLine(got[id]); line != want {
			t.Errorf("%s: %q, want %q", id, line, want)
		}
	}
	b, err := json.Marshal(got["mine"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"quoted_sender_name":"Ana","quoted_text":"Sent image: dinner receipt, 5,27 each"`) {
		t.Fatalf("--json lacks the quote: %s", b)
	}
	if b, _ := json.Marshal(got["photo"]); strings.Contains(string(b), "quoted_") {
		t.Fatalf("a message that quotes nothing carries quote fields: %s", b)
	}
	var out bytes.Buffer
	if err := writeMessageShow(&out, got["mine"]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Quoted sender: Ana (15550000001@s.whatsapp.net)\n") ||
		!strings.Contains(out.String(), "\n> Ana: «Sent image: dinner receipt, 5,27 each»\npaid\n") {
		t.Fatalf("show:\n%s", out.String())
	}
}
