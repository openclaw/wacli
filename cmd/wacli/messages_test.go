package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{input: "hello", max: 10, want: "hello"},
		{input: "hello world", max: 5, want: "hell…"},
		{input: "hello", max: 0, want: "hello"},
		{input: "ab", max: 1, want: "a"},
		{input: "hello\nworld", max: 20, want: "hello world"},
		{input: "  hello  ", max: 20, want: "hello"},
	}
	for _, tc := range tests {
		if got := truncate(tc.input, tc.max); got != tc.want {
			t.Fatalf("truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
		}
	}
}

func TestTruncateForDisplay(t *testing.T) {
	const longID = "3EB0B0E8A1B2C3D4E5F6A7B8C9D0"
	if got := tableCell(longID, 14, true); got != longID {
		t.Fatalf("force full = %q, want %q", got, longID)
	}
	if got := fullTableOutputWithTTY(false, false); !got {
		t.Fatalf("non-TTY should request full output")
	}
	if got := tableCell(longID, 14, false); got != "3EB0B0E8A1B2C…" {
		t.Fatalf("tty truncation = %q", got)
	}
}

func TestMessageContextLinePrefersDisplayText(t *testing.T) {
	got := messageContextLine(store.Message{
		Text:        "raw reaction payload",
		DisplayText: "Reacted 👍 to hello",
	})
	if got != "Reacted 👍 to hello" {
		t.Fatalf("messageContextLine() = %q", got)
	}
}

func TestMessageContextLineFallsBackToText(t *testing.T) {
	got := messageContextLine(store.Message{Text: "hello"})
	if got != "hello" {
		t.Fatalf("messageContextLine() = %q", got)
	}
}

func TestMessageContextLineFallsBackToMedia(t *testing.T) {
	got := messageContextLine(store.Message{MediaType: "IMAGE"})
	if got != "Sent image" {
		t.Fatalf("messageContextLine() = %q", got)
	}
}

func TestMessageFromPrefersSenderName(t *testing.T) {
	got := messageFrom(store.Message{
		SenderJID:  "123456789@lid",
		SenderName: "Alice",
	})
	if got != "Alice" {
		t.Fatalf("messageFrom() = %q, want Alice", got)
	}
}

func TestMessageFromDetailIncludesJID(t *testing.T) {
	got := messageFromDetail(store.Message{
		SenderJID:  "123@s.whatsapp.net",
		SenderName: "Alice",
	})
	if got != "Alice (123@s.whatsapp.net)" {
		t.Fatalf("messageFromDetail() = %q", got)
	}
}

func TestWriteMessagesListFullOutput(t *testing.T) {
	msg := store.Message{
		ChatJID:     "chat@s.whatsapp.net",
		SenderJID:   "sender@s.whatsapp.net",
		MsgID:       "3EB0B0E8A1B2C3D4E5F6A7B8C9D0",
		Timestamp:   time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		DisplayText: "Reacted 👍 to hello",
		Text:        "raw",
	}

	var truncated bytes.Buffer
	if err := writeMessagesList(&truncated, []store.Message{msg}, false); err != nil {
		t.Fatalf("writeMessagesList truncated: %v", err)
	}
	if strings.Contains(truncated.String(), msg.MsgID) {
		t.Fatalf("expected truncated ID, got output:\n%s", truncated.String())
	}

	var full bytes.Buffer
	if err := writeMessagesList(&full, []store.Message{msg}, true); err != nil {
		t.Fatalf("writeMessagesList full: %v", err)
	}
	if !strings.Contains(full.String(), msg.MsgID) {
		t.Fatalf("expected full ID, got output:\n%s", full.String())
	}
	if !strings.Contains(full.String(), "Reacted 👍 to hello") {
		t.Fatalf("expected display text, got output:\n%s", full.String())
	}
}

func TestWriteMessageShowPrefersDisplayTextAndMediaDetails(t *testing.T) {
	msg := store.Message{
		ChatJID:      "chat@s.whatsapp.net",
		SenderJID:    "sender@s.whatsapp.net",
		SenderName:   "Alice",
		MsgID:        "mid",
		Timestamp:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:         "raw payload",
		DisplayText:  "Reacted 👍 to hello",
		MediaType:    "image",
		MediaCaption: "caption",
		Filename:     "pic.jpg",
		MimeType:     "image/jpeg",
		LocalPath:    "/tmp/pic.jpg",
		DownloadedAt: time.Date(2024, 1, 1, 12, 1, 0, 0, time.UTC),
	}

	var out bytes.Buffer
	if err := writeMessageShow(&out, msg); err != nil {
		t.Fatalf("writeMessageShow: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"From: Alice (sender@s.whatsapp.net)",
		"Caption: caption",
		"Filename: pic.jpg",
		"MIME type: image/jpeg",
		"Downloaded: /tmp/pic.jpg",
		"Reacted 👍 to hello",
		"Raw text:\nraw payload",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestMessagesSearchCommandExposesMediaFilters(t *testing.T) {
	cmd := newMessagesSearchCmd(&rootFlags{})
	for _, name := range []string{"has-media", "type", "forwarded"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
	if got := cmd.Flags().Lookup("type").Usage; !strings.Contains(got, "text|image|video|audio|document") {
		t.Fatalf("type usage = %q", got)
	}
}

func TestMessagesListCommandExposesForwardedFilter(t *testing.T) {
	cmd := newMessagesListCmd(&rootFlags{})
	if cmd.Flags().Lookup("forwarded") == nil {
		t.Fatalf("expected --forwarded flag")
	}
}

func TestWriteMessageShowIncludesForwardedMetadata(t *testing.T) {
	msg := store.Message{
		ChatJID:         "chat@s.whatsapp.net",
		SenderJID:       "sender@s.whatsapp.net",
		MsgID:           "mid",
		Timestamp:       time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:            "hello",
		IsForwarded:     true,
		ForwardingScore: 3,
	}

	var out bytes.Buffer
	if err := writeMessageShow(&out, msg); err != nil {
		t.Fatalf("writeMessageShow: %v", err)
	}
	if !strings.Contains(out.String(), "Forwarded: yes") {
		t.Fatalf("expected forwarded marker, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Forwarding score: 3") {
		t.Fatalf("expected forwarding score, got:\n%s", out.String())
	}
}

func TestGetMessageByChatFilterTriesMappedChatJIDs(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pn := "15551234567@s.whatsapp.net"
	lid := "123456789@lid"
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, jid := range []string{pn, lid} {
		if err := db.UpsertChat(jid, "dm", jid, now); err != nil {
			t.Fatalf("UpsertChat %s: %v", jid, err)
		}
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   lid,
		MsgID:     "mid",
		SenderJID: lid,
		Timestamp: now,
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	msg, err := getMessageByChatFilter(db, []string{pn, lid}, "mid")
	if err != nil {
		t.Fatalf("getMessageByChatFilter: %v", err)
	}
	if msg.ChatJID != lid {
		t.Fatalf("ChatJID = %q, want %q", msg.ChatJID, lid)
	}

	msgs, err := getMessageContextByChatFilter(db, []string{pn, lid}, "mid", 1, 1)
	if err != nil {
		t.Fatalf("getMessageContextByChatFilter: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ChatJID != lid {
		t.Fatalf("context = %+v", msgs)
	}
}

func TestResolveMessageSenderNamesUsesLIDMappingAndContacts(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	pn := "15551234567@s.whatsapp.net"
	lid := "123456789@lid"
	if err := db.UpsertContact(pn, "+15551234567", "", "Alice", "", ""); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}
	resolver := fakeLIDResolver{lid: mustParseJID(t, lid), pn: mustParseJID(t, pn)}

	msgs := resolveMessageSenderNamesWith(context.Background(), db, resolver, []store.Message{
		{SenderJID: lid, Text: "hello"},
		{SenderJID: "someone@s.whatsapp.net", Text: "plain"},
		{SenderJID: lid, SenderName: "Existing", Text: "kept"},
	})
	if msgs[0].SenderName != "Alice" {
		t.Fatalf("resolved SenderName = %q, want Alice", msgs[0].SenderName)
	}
	if msgs[1].SenderName != "" {
		t.Fatalf("non-LID SenderName = %q, want empty", msgs[1].SenderName)
	}
	if msgs[2].SenderName != "Existing" {
		t.Fatalf("existing SenderName = %q", msgs[2].SenderName)
	}
}

type fakeLIDResolver struct {
	lid types.JID
	pn  types.JID
}

func (f fakeLIDResolver) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	if jid == f.lid {
		return f.pn
	}
	return jid
}

func mustParseJID(t *testing.T, s string) types.JID {
	t.Helper()
	jid, err := types.ParseJID(s)
	if err != nil {
		t.Fatalf("ParseJID(%q): %v", s, err)
	}
	return jid
}
