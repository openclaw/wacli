package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
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

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("🙂🙂🙂", 2)
	if got != "🙂…" {
		t.Fatalf("truncate emoji = %q, want first rune plus ellipsis", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
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

func TestWriteCallsList(t *testing.T) {
	call := store.CallEvent{
		ChatJID:      "chat@s.whatsapp.net",
		ChatName:     "Alice",
		CallID:       "call-1234567890",
		EventType:    "call_log",
		Direction:    "outbound",
		Media:        "audio",
		Outcome:      "connected",
		DurationSecs: 61,
		Timestamp:    time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	var out bytes.Buffer
	if err := writeCallsList(&out, []store.CallEvent{call}, true); err != nil {
		t.Fatalf("writeCallsList: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Alice", "outbound", "audio", "call_log", "connected (1m01s)", "call-1234567890"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestCallsListCommandHasExpectedFlags(t *testing.T) {
	cmd := newCallsListCmd(&rootFlags{})
	for _, name := range []string{"chat", "limit", "after", "before", "asc"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
}

func TestWriteMessageShowPrefersDisplayTextAndMediaDetails(t *testing.T) {
	msg := store.Message{
		ChatJID:         "chat@s.whatsapp.net",
		SenderJID:       "sender@s.whatsapp.net",
		SenderName:      "Alice",
		MsgID:           "mid",
		Timestamp:       time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:            "raw payload",
		DisplayText:     "Reacted 👍 to hello",
		QuotedMsgID:     "quoted",
		QuotedSenderJID: "quoted-sender@s.whatsapp.net",
		MediaType:       "image",
		MediaCaption:    "caption",
		Filename:        "pic.jpg",
		MimeType:        "image/jpeg",
		LocalPath:       "/tmp/pic.jpg",
		DownloadedAt:    time.Date(2024, 1, 1, 12, 1, 0, 0, time.UTC),
	}

	var out bytes.Buffer
	if err := writeMessageShow(&out, msg); err != nil {
		t.Fatalf("writeMessageShow: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"From: Alice (sender@s.whatsapp.net)",
		"Quoted message: quoted",
		"Quoted sender: quoted-sender@s.whatsapp.net",
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

func TestWriteMessageShowPreservesMultilineBody(t *testing.T) {
	msg := store.Message{
		ChatJID:     "chat@s.whatsapp.net",
		SenderJID:   "sender@s.whatsapp.net",
		MsgID:       "mid",
		Timestamp:   time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		Text:        "line 1\nline 2\tindented\x1b[31m",
		DisplayText: "display 1\ndisplay 2",
	}

	var out bytes.Buffer
	if err := writeMessageShow(&out, msg); err != nil {
		t.Fatalf("writeMessageShow: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"\ndisplay 1\ndisplay 2\n",
		"Raw text:\nline 1\nline 2\tindented\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("output kept escape sequence:\n%q", got)
	}
}

func TestMessagesSearchCommandExposesMediaFilters(t *testing.T) {
	cmd := newMessagesSearchCmd(&rootFlags{})
	for _, name := range []string{"has-media", "type", "forwarded", "starred"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
	if got := cmd.Flags().Lookup("type").Usage; !strings.Contains(got, "text|image|video|audio|document") {
		t.Fatalf("type usage = %q", got)
	}
}

func TestMessagesListCommandExposesMessageFilters(t *testing.T) {
	cmd := newMessagesListCmd(&rootFlags{})
	for _, name := range []string{"forwarded", "starred"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
}

func TestMessagesStarredCommandExposesFilters(t *testing.T) {
	cmd := newMessagesStarredCmd(&rootFlags{})
	for _, name := range []string{"chat", "limit", "after", "before", "asc"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
}

func TestMessagesExportCommandExposesDateFilters(t *testing.T) {
	cmd := newMessagesExportCmd(&rootFlags{})
	for _, name := range []string{"after", "before", "output"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
}

func TestMessagesMutationCommandsExposeSafetyFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{
		newMessagesDeleteCmd(&rootFlags{}),
		newMessagesEditCmd(&rootFlags{}),
	} {
		for _, name := range []string{"chat", "id", "post-send-wait"} {
			if cmd.Flags().Lookup(name) == nil {
				t.Fatalf("%s missing --%s", cmd.Name(), name)
			}
		}
	}
	if newMessagesEditCmd(&rootFlags{}).Flags().Lookup("message") == nil {
		t.Fatalf("edit missing --message")
	}
}

func TestMessagesDeleteRejectsReadOnlyBeforeOpeningStore(t *testing.T) {
	cmd := newMessagesDeleteCmd(&rootFlags{readOnly: true})
	cmd.SetArgs([]string{"--chat", "+15551234567", "--id", "mid"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatalf("error = %v, want read-only", err)
	}
}

func TestMessagesEditValidation(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 30, 0, 0, time.UTC)
	msg := store.Message{
		MsgID:     "mid",
		Timestamp: now.Add(-time.Minute),
		FromMe:    true,
		Text:      "old",
	}
	if err := validateMessageCanEdit(msg, now); err != nil {
		t.Fatalf("validateMessageCanEdit: %v", err)
	}

	msg.FromMe = false
	if err := validateMessageCanEdit(msg, now); err == nil || !strings.Contains(err.Error(), "not sent by me") {
		t.Fatalf("from-them error = %v", err)
	}

	msg.FromMe = true
	msg.DeletedForMe = true
	msg.Timestamp = now.Add(-time.Minute)
	if err := validateMessageCanEdit(msg, now); err == nil || !strings.Contains(err.Error(), "deleted for me") {
		t.Fatalf("deleted-for-me error = %v", err)
	}

	msg.DeletedForMe = false
	msg.Timestamp = now.Add(-21 * time.Minute)
	if err := validateMessageCanEdit(msg, now); err == nil || !strings.Contains(err.Error(), "edit window") {
		t.Fatalf("old message error = %v", err)
	}
}

func TestMessagesDeleteForMeValidation(t *testing.T) {
	msg := store.Message{MsgID: "mid", FromMe: false}
	if err := validateMessageCanDeleteForMe(msg); err != nil {
		t.Fatalf("validateMessageCanDeleteForMe: %v", err)
	}
	if err := validateMessageCanRevoke(msg); err == nil || !strings.Contains(err.Error(), "not sent by me") {
		t.Fatalf("revoke from-them error = %v", err)
	}

	msg.DeletedForMe = true
	if err := validateMessageCanDeleteForMe(msg); err == nil || !strings.Contains(err.Error(), "deleted for me") {
		t.Fatalf("deleted-for-me error = %v", err)
	}
}

func TestMessagesExportCommandAppliesDateFilters(t *testing.T) {
	storeDir := t.TempDir()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	chat := "chat@s.whatsapp.net"
	base := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for _, row := range []store.UpsertMessageParams{
		{ChatJID: chat, MsgID: "before", SenderJID: chat, Timestamp: base, Text: "before"},
		{ChatJID: chat, MsgID: "inside-1", SenderJID: chat, Timestamp: base.Add(time.Second), Text: "inside 1"},
		{ChatJID: chat, MsgID: "inside-2", SenderJID: chat, Timestamp: base.Add(2 * time.Second), Text: "inside 2"},
		{ChatJID: chat, MsgID: "after", SenderJID: chat, Timestamp: base.Add(3 * time.Second), Text: "after"},
	} {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatalf("UpsertMessage %s: %v", row.MsgID, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	output := filepath.Join(storeDir, "export.json")
	cmd := newMessagesExportCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	cmd.SetArgs([]string{
		"--chat", chat,
		"--after", base.Format(time.RFC3339),
		"--before", base.Add(3 * time.Second).Format(time.RFC3339),
		"--output", output,
		"--limit", "10",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("messages export: %v", err)
	}

	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output mode = %04o, want 0600", got)
	}
	var got struct {
		Success bool `json:"success"`
		Data    struct {
			Messages []store.Message `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal export: %v\n%s", err, string(raw))
	}
	if !got.Success {
		t.Fatalf("success = false")
	}
	if gotIDs := messageIDs(got.Data.Messages); gotIDs != "inside-1,inside-2" {
		t.Fatalf("exported ids = %s", gotIDs)
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

func TestDeleteLocalMediaIfRequestedReportsActualRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	deleted, err := deleteLocalMediaIfRequested(true, path)
	if err != nil {
		t.Fatalf("deleteLocalMediaIfRequested: %v", err)
	}
	if !deleted {
		t.Fatal("deleted = false, want true")
	}
	deleted, err = deleteLocalMediaIfRequested(true, path)
	if err != nil {
		t.Fatalf("delete stale media path: %v", err)
	}
	if deleted {
		t.Fatal("deleted stale path = true, want false")
	}
	deleted, err = deleteLocalMediaIfRequested(false, path)
	if err != nil || deleted {
		t.Fatalf("delete disabled = %v, %v; want false, nil", deleted, err)
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

func TestMessageChatJIDFilterUsesReadOnlySessionLIDMap(t *testing.T) {
	storeDir := t.TempDir()
	writer, err := app.New(app.Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}
	writer.Close()
	writeTestSessionLIDMap(t, filepath.Join(storeDir, "session.db"), "999123456789", "15551234567")

	reader, err := app.New(app.Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()

	got, err := messageChatJIDFilter(context.Background(), reader, "15551234567@s.whatsapp.net")
	if err != nil {
		t.Fatalf("messageChatJIDFilter: %v", err)
	}
	want := "15551234567@s.whatsapp.net,999123456789@lid"
	if strings.Join(got, ",") != want {
		t.Fatalf("jids = %v, want %s", got, want)
	}
	assertNoSQLiteSidecars(t, filepath.Join(storeDir, "session.db"))
}

type fakeLIDResolver struct {
	lid types.JID
	pn  types.JID
}

func writeTestSessionLIDMap(t *testing.T, path, lid, pn string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("Open session sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
		CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL);
		INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?);
	`, lid, pn)
	if err != nil {
		t.Fatalf("seed session LID map: %v", err)
	}
}

func assertNoSQLiteSidecars(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		sidecar := path + suffix
		if _, err := os.Stat(sidecar); err == nil {
			t.Fatalf("unexpected SQLite sidecar %s", sidecar)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", sidecar, err)
		}
	}
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

func messageIDs(msgs []store.Message) string {
	ids := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		ids = append(ids, msg.MsgID)
	}
	return strings.Join(ids, ",")
}
