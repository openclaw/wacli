package main

import (
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/linkpreview"
	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

func openSendTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/wacli.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type recipientTestApp struct {
	db *store.DB
}

func (a recipientTestApp) DB() *store.DB {
	return a.db
}

func TestResolveRecipientFallsBackToFormattedPhone(t *testing.T) {
	db := openSendTestDB(t)

	got, err := resolveRecipient(recipientTestApp{db: db}, "+1 (555) 123-4567", recipientOptions{})
	if err != nil {
		t.Fatalf("resolveRecipient: %v", err)
	}
	if got.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("recipient = %q", got.String())
	}
}

func TestResolveRecipientUsesContactAlias(t *testing.T) {
	db := openSendTestDB(t)
	if err := db.UpsertContact("15551234567@s.whatsapp.net", "15551234567", "Alice", "", "", ""); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}
	if err := db.SetAlias("15551234567@s.whatsapp.net", "mom"); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}

	got, err := resolveRecipient(recipientTestApp{db: db}, "mom", recipientOptions{})
	if err != nil {
		t.Fatalf("resolveRecipient: %v", err)
	}
	if got.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("recipient = %q", got.String())
	}
}

func TestResolveRecipientNumericGroupNameBeatsPhoneFallback(t *testing.T) {
	db := openSendTestDB(t)
	if err := db.UpsertGroup("12345@g.us", "12345", "", time.Now()); err != nil {
		t.Fatalf("UpsertGroup: %v", err)
	}

	got, err := resolveRecipient(recipientTestApp{db: db}, "12345", recipientOptions{})
	if err != nil {
		t.Fatalf("resolveRecipient: %v", err)
	}
	if got.String() != "12345@g.us" {
		t.Fatalf("recipient = %q", got.String())
	}
}

func TestResolveRecipientNumericDirectChatDoesNotHijackPhone(t *testing.T) {
	db := openSendTestDB(t)
	if err := db.UpsertChat("999@s.whatsapp.net", "dm", "1234567", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	got, err := resolveRecipient(recipientTestApp{db: db}, "1234567", recipientOptions{})
	if err != nil {
		t.Fatalf("resolveRecipient: %v", err)
	}
	if got.String() != "1234567@s.whatsapp.net" {
		t.Fatalf("recipient = %q", got.String())
	}
}

func TestResolveRecipientAmbiguousRequiresPickWhenNonInteractive(t *testing.T) {
	db := openSendTestDB(t)
	if err := db.UpsertContact("1@s.whatsapp.net", "1", "", "John", "", ""); err != nil {
		t.Fatalf("UpsertContact 1: %v", err)
	}
	if err := db.UpsertContact("2@s.whatsapp.net", "2", "", "Johnny", "", ""); err != nil {
		t.Fatalf("UpsertContact 2: %v", err)
	}

	_, err := resolveRecipient(recipientTestApp{db: db}, "John", recipientOptions{})
	if err == nil || !strings.Contains(err.Error(), "use --pick N") {
		t.Fatalf("expected --pick ambiguity, got %v", err)
	}
	if !strings.Contains(err.Error(), "1)") || !strings.Contains(err.Error(), "2)") {
		t.Fatalf("expected numbered candidates, got %v", err)
	}
}

func TestResolveRecipientPickSelectsCandidate(t *testing.T) {
	db := openSendTestDB(t)
	if err := db.UpsertContact("1@s.whatsapp.net", "1", "", "John", "", ""); err != nil {
		t.Fatalf("UpsertContact 1: %v", err)
	}
	if err := db.UpsertContact("2@s.whatsapp.net", "2", "", "Johnny", "", ""); err != nil {
		t.Fatalf("UpsertContact 2: %v", err)
	}

	got, err := resolveRecipient(recipientTestApp{db: db}, "John", recipientOptions{pick: 2})
	if err != nil {
		t.Fatalf("resolveRecipient: %v", err)
	}
	if got.String() != "2@s.whatsapp.net" {
		t.Fatalf("recipient = %q", got.String())
	}
}

func TestResolveReplySenderFromStore(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	sender := "15551234567@s.whatsapp.net"

	if err := db.UpsertChat(chat.String(), "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "quoted",
		SenderJID: sender,
		Timestamp: time.Now(),
		Text:      "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	got, err := resolveReplySender(db, chat, "quoted", "")
	if err != nil {
		t.Fatalf("resolveReplySender: %v", err)
	}
	if got.String() != sender {
		t.Fatalf("sender = %q, want %q", got.String(), sender)
	}
}

func TestResolveReplySenderOverride(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}

	got, err := resolveReplySender(db, chat, "missing", "+15551234567")
	if err != nil {
		t.Fatalf("resolveReplySender: %v", err)
	}
	if got.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("sender = %q", got.String())
	}
}

func TestResolveReplySenderRequiresGroupSenderWhenMissing(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}

	_, err := resolveReplySender(db, chat, "missing", "")
	if err == nil || !strings.Contains(err.Error(), "--reply-to-sender is required") {
		t.Fatalf("expected group sender error, got %v", err)
	}
}

func TestResolveReplySenderAllowsDirectMessageWithoutSender(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	got, err := resolveReplySender(db, chat, "missing", "")
	if err != nil {
		t.Fatalf("resolveReplySender: %v", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("expected empty sender for direct reply, got %q", got.String())
	}
}

func TestUpsertSentReactionStoresDisplayText(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	now := time.Date(2026, 5, 5, 6, 30, 0, 0, time.UTC)

	if err := db.UpsertChat(chat.String(), "dm", "Alice", now); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "target",
		Timestamp: now.Add(-time.Second),
		FromMe:    true,
		Text:      "hello reaction target",
	}); err != nil {
		t.Fatalf("UpsertMessage target: %v", err)
	}

	upsertSentReaction(db, chat, "Alice", "react1", "target", "👍", now)

	msg, err := db.GetMessage(chat.String(), "react1")
	if err != nil {
		t.Fatalf("GetMessage reaction: %v", err)
	}
	if !msg.FromMe || msg.SenderName != "me" {
		t.Fatalf("unexpected sender fields: from_me=%v sender=%q", msg.FromMe, msg.SenderName)
	}
	if msg.ReactionToID != "target" || msg.ReactionEmoji != "👍" {
		t.Fatalf("unexpected reaction fields: to=%q emoji=%q", msg.ReactionToID, msg.ReactionEmoji)
	}
	if msg.DisplayText != "Reacted 👍 to hello reaction target" {
		t.Fatalf("display text = %q", msg.DisplayText)
	}
}

func TestBuildReplyContextInfo(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}

	got, err := buildReplyContextInfo(db, chat, "quoted", "+15551234567")
	if err != nil {
		t.Fatalf("buildReplyContextInfo: %v", err)
	}
	if got.GetStanzaID() != "quoted" {
		t.Fatalf("stanza ID = %q, want quoted", got.GetStanzaID())
	}
	if got.GetParticipant() != "15551234567@s.whatsapp.net" {
		t.Fatalf("participant = %q", got.GetParticipant())
	}

	got, err = buildReplyContextInfo(db, chat, "", "+15551234567")
	if err != nil {
		t.Fatalf("empty buildReplyContextInfo: %v", err)
	}
	if got != nil {
		t.Fatalf("empty reply context = %v, want nil", got)
	}
}

func TestParseMentionedJIDs(t *testing.T) {
	got, err := parseMentionedJIDs([]string{
		" +1 (555) 123-4567 ",
		"15551234567@s.whatsapp.net",
		"15557654321@s.whatsapp.net",
		"",
	})
	if err != nil {
		t.Fatalf("parseMentionedJIDs: %v", err)
	}
	want := []string{"15551234567@s.whatsapp.net", "15557654321@s.whatsapp.net"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mentions = %v, want %v", got, want)
	}
}

func TestParseMentionedJIDsRejectsGroupJID(t *testing.T) {
	_, err := parseMentionedJIDs([]string{"12345@g.us"})
	if err == nil || !strings.Contains(err.Error(), "mentions must target a user") {
		t.Fatalf("expected group mention rejection, got %v", err)
	}
}

func TestSendTextCommandExposesNoPreviewFlag(t *testing.T) {
	cmd := newSendTextCmd(&rootFlags{})
	if cmd.Flags().Lookup("no-preview") == nil {
		t.Fatalf("missing --no-preview flag")
	}
}

func TestSendTextCommandExposesMessageEscapesFlag(t *testing.T) {
	cmd := newSendTextCmd(&rootFlags{})
	if cmd.Flags().Lookup("message-escapes") == nil {
		t.Fatalf("missing --message-escapes flag")
	}
}

func TestSendTextCommandExposesMentionFlag(t *testing.T) {
	cmd := newSendTextCmd(&rootFlags{})
	if cmd.Flags().Lookup("mention") == nil {
		t.Fatalf("missing --mention flag")
	}
}

func TestDecodeMessageEscapes(t *testing.T) {
	got, err := decodeMessageEscapes(`line1\nline2\ttab\rcr\\slash\"quote`)
	if err != nil {
		t.Fatalf("decodeMessageEscapes: %v", err)
	}
	want := "line1\nline2\ttab\rcr\\slash\"quote"
	if got != want {
		t.Fatalf("decoded = %q, want %q", got, want)
	}
}

func TestDecodeMessageEscapesRejectsUnknownEscape(t *testing.T) {
	_, err := decodeMessageEscapes(`hello\q`)
	if err == nil || !strings.Contains(err.Error(), `unsupported escape sequence \q`) {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildTextMessageUsesPlainConversationWithoutReplyOrPreview(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	msg, plain, err := buildTextMessage(db, chat, "hello", "", "", nil, nil)
	if err != nil {
		t.Fatalf("buildTextMessage: %v", err)
	}
	if !plain {
		t.Fatalf("plain = false, want true")
	}
	if msg != nil {
		t.Fatalf("msg = %v, want nil", msg)
	}
}

func TestBuildTextMessageAttachesMentions(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	mentions := []string{"15551234567@s.whatsapp.net", "15557654321@s.whatsapp.net"}

	msg, plain, err := buildTextMessage(db, chat, "hey @15551234567", "", "", nil, mentions)
	if err != nil {
		t.Fatalf("buildTextMessage: %v", err)
	}
	if plain {
		t.Fatalf("plain = true, want false")
	}
	ext := msg.GetExtendedTextMessage()
	if ext.GetText() != "hey @15551234567" {
		t.Fatalf("text = %q", ext.GetText())
	}
	got := ext.GetContextInfo().GetMentionedJID()
	if strings.Join(got, ",") != strings.Join(mentions, ",") {
		t.Fatalf("mentioned JIDs = %v, want %v", got, mentions)
	}
}

func TestBuildTextMessageCombinesReplyAndMentions(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}

	msg, plain, err := buildTextMessage(db, chat, "replying @15551234567", "quoted", "+15557654321", nil, []string{"15551234567@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("buildTextMessage: %v", err)
	}
	if plain {
		t.Fatalf("plain = true, want false")
	}
	info := msg.GetExtendedTextMessage().GetContextInfo()
	if info.GetStanzaID() != "quoted" {
		t.Fatalf("stanza ID = %q, want quoted", info.GetStanzaID())
	}
	if info.GetParticipant() != "15557654321@s.whatsapp.net" {
		t.Fatalf("participant = %q", info.GetParticipant())
	}
	if got := info.GetMentionedJID(); strings.Join(got, ",") != "15551234567@s.whatsapp.net" {
		t.Fatalf("mentioned JIDs = %v", got)
	}
}

func TestBuildTextMessageAttachesLinkPreview(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	preview := &linkpreview.Preview{
		URL:         "https://example.com/post",
		Title:       "Example",
		Description: "Description",
		Thumbnail:   []byte("jpeg"),
	}

	msg, plain, err := buildTextMessage(db, chat, "see https://example.com/post", "", "", preview, nil)
	if err != nil {
		t.Fatalf("buildTextMessage: %v", err)
	}
	if plain {
		t.Fatalf("plain = true, want false")
	}
	ext := msg.GetExtendedTextMessage()
	if ext.GetText() != "see https://example.com/post" {
		t.Fatalf("text = %q", ext.GetText())
	}
	if ext.GetMatchedText() != preview.URL {
		t.Fatalf("matched text = %q", ext.GetMatchedText())
	}
	if ext.GetTitle() != preview.Title {
		t.Fatalf("title = %q", ext.GetTitle())
	}
	if ext.GetDescription() != preview.Description {
		t.Fatalf("description = %q", ext.GetDescription())
	}
	if ext.GetPreviewType() != waProto.ExtendedTextMessage_IMAGE {
		t.Fatalf("preview type = %v", ext.GetPreviewType())
	}
	if string(ext.GetJPEGThumbnail()) != "jpeg" {
		t.Fatalf("thumbnail = %q", string(ext.GetJPEGThumbnail()))
	}
}
