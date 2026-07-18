package main

import (
	"context"
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

type recordingTextSender struct {
	textCalls       int
	text            string
	protoCalls      int
	protoMsg        *waProto.Message
	protoRecipient  types.JID
	textRecipient   types.JID
	nextTextID      types.MessageID
	nextProtoMsgID  types.MessageID
	groupInfo       *types.GroupInfo
	groupInfoCalls  int
	linkedJID       string
	linkedLID       types.JID
	resolveLIDCalls int
}

func (s *recordingTextSender) SendText(_ context.Context, to types.JID, text string) (types.MessageID, error) {
	s.textCalls++
	s.textRecipient = to
	s.text = text
	if s.nextTextID != "" {
		return s.nextTextID, nil
	}
	return "text-id", nil
}

func (s *recordingTextSender) SendProtoMessage(_ context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	s.protoCalls++
	s.protoRecipient = to
	s.protoMsg = msg
	if s.nextProtoMsgID != "" {
		return s.nextProtoMsgID, nil
	}
	return "proto-id", nil
}

func (s *recordingTextSender) GetGroupInfo(_ context.Context, _ types.JID) (*types.GroupInfo, error) {
	s.groupInfoCalls++
	return s.groupInfo, nil
}

func (s *recordingTextSender) LinkedJID() string {
	return s.linkedJID
}

func (s *recordingTextSender) ResolvePNToLID(_ context.Context, _ types.JID) types.JID {
	s.resolveLIDCalls++
	return s.linkedLID
}

func requireExtendedText(t *testing.T, msg *waProto.Message) *waProto.ExtendedTextMessage {
	t.Helper()
	if msg.GetEphemeralMessage() != nil {
		t.Fatalf("unexpected EphemeralMessage wrapper")
	}
	ext := msg.GetExtendedTextMessage()
	if ext == nil {
		t.Fatalf("missing ExtendedTextMessage")
	}
	return ext
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

func TestBuildTextReplyContextInfo(t *testing.T) {
	self := "15550000000@s.whatsapp.net"
	tests := []struct {
		name        string
		chat        types.JID
		fromMe      bool
		senderJID   string
		participant string
	}{
		{
			name:        "direct incoming",
			chat:        types.JID{User: "15551234567", Server: types.DefaultUserServer},
			senderJID:   "15551234567@s.whatsapp.net",
			participant: "15551234567@s.whatsapp.net",
		},
		{
			name:        "group incoming",
			chat:        types.JID{User: "12345", Server: types.GroupServer},
			senderJID:   "15551234567@s.whatsapp.net",
			participant: "15551234567@s.whatsapp.net",
		},
		{
			name:        "direct outgoing",
			chat:        types.JID{User: "15551234567", Server: types.DefaultUserServer},
			fromMe:      true,
			participant: self,
		},
		{
			name:        "group outgoing",
			chat:        types.JID{User: "12345", Server: types.GroupServer},
			fromMe:      true,
			participant: self,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openSendTestDB(t)
			if err := db.UpsertChat(tc.chat.String(), "chat", tc.name, time.Now()); err != nil {
				t.Fatalf("UpsertChat: %v", err)
			}
			if err := db.UpsertMessage(store.UpsertMessageParams{
				ChatJID:   tc.chat.String(),
				MsgID:     "quoted",
				SenderJID: tc.senderJID,
				Timestamp: time.Now(),
				FromMe:    tc.fromMe,
				Text:      "quoted text",
			}); err != nil {
				t.Fatalf("UpsertMessage: %v", err)
			}

			got, err := buildTextReplyContextInfo(db, tc.chat, "quoted", "", self)
			if err != nil {
				t.Fatalf("buildTextReplyContextInfo: %v", err)
			}
			if got.GetStanzaID() != "quoted" {
				t.Fatalf("stanza ID = %q, want quoted", got.GetStanzaID())
			}
			if got.GetParticipant() != tc.participant {
				t.Fatalf("participant = %q, want %q", got.GetParticipant(), tc.participant)
			}
			if got.GetQuotedMessage().GetConversation() != "quoted text" {
				t.Fatalf("quoted text = %q", got.GetQuotedMessage().GetConversation())
			}
		})
	}
}

func TestSendTextMessageRejectsUnconstructableQuotesBeforeSending(t *testing.T) {
	tests := []struct {
		name      string
		seed      func(*testing.T, *store.DB, types.JID)
		wantError string
	}{
		{
			name:      "missing row",
			wantError: "not found in local store",
		},
		{
			name: "unsupported media type",
			seed: func(t *testing.T, db *store.DB, chat types.JID) {
				t.Helper()
				if err := db.UpsertMessage(store.UpsertMessageParams{
					ChatJID:   chat.String(),
					MsgID:     "quoted",
					SenderJID: chat.String(),
					Timestamp: time.Now(),
					MediaType: "location",
				}); err != nil {
					t.Fatalf("UpsertMessage: %v", err)
				}
			},
			wantError: "unsupported stored media type",
		},
		{
			name: "incomplete document metadata",
			seed: func(t *testing.T, db *store.DB, chat types.JID) {
				t.Helper()
				if err := db.UpsertMessage(store.UpsertMessageParams{
					ChatJID:   chat.String(),
					MsgID:     "quoted",
					SenderJID: chat.String(),
					Timestamp: time.Now(),
					MediaType: "document",
					Filename:  "incomplete.pdf",
					MimeType:  "application/pdf",
				}); err != nil {
					t.Fatalf("UpsertMessage: %v", err)
				}
			},
			wantError: "incomplete media metadata",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openSendTestDB(t)
			chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
			if err := db.UpsertChat(chat.String(), "dm", "Alice", time.Now()); err != nil {
				t.Fatalf("UpsertChat: %v", err)
			}
			if tc.seed != nil {
				tc.seed(t, db, chat)
			}
			sender := &recordingTextSender{linkedJID: "15550000000@s.whatsapp.net"}

			_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "reply", "quoted", "", nil, nil, textEphemeralOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if sender.textCalls != 0 || sender.protoCalls != 0 {
				t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/0", sender.textCalls, sender.protoCalls)
			}
		})
	}
}

func TestSendTextMessageQuotesStoredDocument(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	if err := db.UpsertChat(chat.String(), "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chat.String(),
		MsgID:         "quoted-document",
		Timestamp:     time.Now(),
		FromMe:        true,
		MediaType:     "document",
		MediaCaption:  "test document",
		Filename:      "test.pdf",
		MimeType:      "application/pdf",
		DirectPath:    "/v/t62/test-document",
		MediaKey:      []byte("media-key"),
		FileSHA256:    []byte("plain-hash"),
		FileEncSHA256: []byte("encrypted-hash"),
		FileLength:    1234,
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	sender := &recordingTextSender{linkedJID: "15550000000@s.whatsapp.net"}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "reply", "quoted-document", "15550000000@s.whatsapp.net", nil, nil, textEphemeralOptions{})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	info := requireExtendedText(t, sender.protoMsg).GetContextInfo()
	if info.GetStanzaID() != "quoted-document" {
		t.Fatalf("stanza ID = %q, want quoted-document", info.GetStanzaID())
	}
	if info.GetParticipant() != "15550000000@s.whatsapp.net" {
		t.Fatalf("participant = %q", info.GetParticipant())
	}
	doc := info.GetQuotedMessage().GetDocumentMessage()
	if doc == nil {
		t.Fatal("quoted document message is nil")
	}
	if doc.GetFileName() != "test.pdf" || doc.GetTitle() != "test.pdf" || doc.GetMimetype() != "application/pdf" {
		t.Fatalf("quoted document identity: filename=%q title=%q mime=%q", doc.GetFileName(), doc.GetTitle(), doc.GetMimetype())
	}
	if doc.GetCaption() != "test document" || doc.GetDirectPath() != "/v/t62/test-document" || doc.GetFileLength() != 1234 {
		t.Fatalf("quoted document metadata: caption=%q direct_path=%q length=%d", doc.GetCaption(), doc.GetDirectPath(), doc.GetFileLength())
	}
	if string(doc.GetMediaKey()) != "media-key" || string(doc.GetFileSHA256()) != "plain-hash" || string(doc.GetFileEncSHA256()) != "encrypted-hash" {
		t.Fatal("quoted document media hashes or key were not preserved")
	}
	if sender.textCalls != 0 || sender.protoCalls != 1 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/1", sender.textCalls, sender.protoCalls)
	}
}

func TestSendTextMessageReplySenderBypassesSelfLIDLookup(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	if err := db.UpsertChat(chat.String(), "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "quoted",
		Timestamp: time.Now(),
		FromMe:    true,
		Text:      "quoted text",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	sender := &recordingTextSender{
		linkedJID: "15550000000@s.whatsapp.net",
		groupInfo: &types.GroupInfo{AddressingMode: types.AddressingModeLID},
	}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "reply", "quoted", "15551234567:4@s.whatsapp.net", nil, nil, textEphemeralOptions{})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	info := requireExtendedText(t, sender.protoMsg).GetContextInfo()
	if got := info.GetParticipant(); got != "15551234567@s.whatsapp.net" {
		t.Fatalf("participant = %q", got)
	}
	if sender.groupInfoCalls != 0 || sender.resolveLIDCalls != 0 {
		t.Fatalf("identity calls: GetGroupInfo=%d ResolvePNToLID=%d, want 0/0", sender.groupInfoCalls, sender.resolveLIDCalls)
	}
}

func TestSendTextMessageAllowsUnsyncedGroupReplyWithSender(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	sender := &recordingTextSender{linkedJID: "15550000000@s.whatsapp.net"}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "reply", "quoted", "+15551234567", nil, nil, textEphemeralOptions{})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	if sender.protoCalls != 1 || sender.textCalls != 0 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/1", sender.textCalls, sender.protoCalls)
	}
	info := requireExtendedText(t, sender.protoMsg).GetContextInfo()
	if info.GetStanzaID() != "quoted" {
		t.Fatalf("stanza ID = %q, want quoted", info.GetStanzaID())
	}
	if info.GetParticipant() != "15551234567@s.whatsapp.net" {
		t.Fatalf("participant = %q", info.GetParticipant())
	}
	if info.GetQuotedMessage() != nil {
		t.Fatalf("quoted message = %v, want nil without stored content", info.GetQuotedMessage())
	}
}

func TestSendTextMessageUsesLinkedLIDForOutgoingQuoteInLIDGroup(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	if err := db.UpsertChat(chat.String(), "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "quoted",
		Timestamp: time.Now(),
		FromMe:    true,
		Text:      "quoted text",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	sender := &recordingTextSender{
		linkedJID: "15550000000@s.whatsapp.net",
		linkedLID: types.JID{User: "987654321", Server: types.HiddenUserServer},
		groupInfo: &types.GroupInfo{AddressingMode: types.AddressingModeLID},
	}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "reply", "quoted", "", nil, nil, textEphemeralOptions{})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	info := requireExtendedText(t, sender.protoMsg).GetContextInfo()
	if got := info.GetParticipant(); got != sender.linkedLID.String() {
		t.Fatalf("participant = %q, want %q", got, sender.linkedLID.String())
	}
	if sender.groupInfoCalls != 1 || sender.resolveLIDCalls != 1 {
		t.Fatalf("identity calls: GetGroupInfo=%d ResolvePNToLID=%d, want 1/1", sender.groupInfoCalls, sender.resolveLIDCalls)
	}
}

func TestSendTextMessageKeepsStoredSenderForIncomingQuoteInLIDGroup(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	senderJID := "987654321:4@lid"
	wantSenderJID := "987654321@lid"
	if err := db.UpsertChat(chat.String(), "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "quoted",
		SenderJID: senderJID,
		Timestamp: time.Now(),
		Text:      "quoted text",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	sender := &recordingTextSender{
		linkedJID: "15550000000@s.whatsapp.net",
		linkedLID: types.JID{User: "123456789", Server: types.HiddenUserServer},
		groupInfo: &types.GroupInfo{AddressingMode: types.AddressingModeLID},
	}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "reply", "quoted", "", nil, nil, textEphemeralOptions{})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	info := requireExtendedText(t, sender.protoMsg).GetContextInfo()
	if got := info.GetParticipant(); got != wantSenderJID {
		t.Fatalf("participant = %q, want %q", got, wantSenderJID)
	}
	if sender.groupInfoCalls != 0 || sender.resolveLIDCalls != 0 {
		t.Fatalf("identity calls: GetGroupInfo=%d ResolvePNToLID=%d, want 0/0", sender.groupInfoCalls, sender.resolveLIDCalls)
	}
}

func TestSendTextMessageUsesLinkedLIDForOutgoingQuoteInLIDChat(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "987654321", Server: types.HiddenUserServer}
	if err := db.UpsertChat(chat.String(), "dm", "Alice", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "quoted",
		Timestamp: time.Now(),
		FromMe:    true,
		Text:      "quoted text",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	sender := &recordingTextSender{
		linkedJID: "15550000000@s.whatsapp.net",
		linkedLID: types.JID{User: "123456789", Server: types.HiddenUserServer},
	}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "reply", "quoted", "", nil, nil, textEphemeralOptions{})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	info := requireExtendedText(t, sender.protoMsg).GetContextInfo()
	if got := info.GetParticipant(); got != sender.linkedLID.String() {
		t.Fatalf("participant = %q, want %q", got, sender.linkedLID.String())
	}
	if sender.groupInfoCalls != 0 || sender.resolveLIDCalls != 1 {
		t.Fatalf("identity calls: GetGroupInfo=%d ResolvePNToLID=%d, want 0/1", sender.groupInfoCalls, sender.resolveLIDCalls)
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

func TestSendTextCommandExposesEphemeralFlag(t *testing.T) {
	cmd := newSendTextCmd(&rootFlags{})
	if cmd.Flags().Lookup("ephemeral") == nil {
		t.Fatalf("missing --ephemeral flag")
	}
	if cmd.Flags().Lookup("ephemeral-duration") == nil {
		t.Fatalf("missing --ephemeral-duration flag")
	}
	if got := cmd.Flags().Lookup("ephemeral-duration").DefValue; got != "" {
		t.Fatalf("ephemeral-duration default = %q, want empty", got)
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

func TestSendTextMessageKeepsPlainTextFastPathWithoutEphemeral(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	sender := &recordingTextSender{}

	id, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "hello", "", "", nil, nil, textEphemeralOptions{})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	if id != "text-id" {
		t.Fatalf("id = %q, want text-id", id)
	}
	if sender.textCalls != 1 || sender.protoCalls != 0 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 1/0", sender.textCalls, sender.protoCalls)
	}
	if sender.textRecipient != chat || sender.text != "hello" {
		t.Fatalf("plain send = (%s, %q), want (%s, hello)", sender.textRecipient, sender.text, chat)
	}
}

func TestSendTextMessageUsesDefaultEphemeralExpirationForPrivateEphemeralWithoutDuration(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	sender := &recordingTextSender{}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "hello", "", "", nil, nil, textEphemeralOptions{Enabled: true})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	if sender.textCalls != 0 || sender.protoCalls != 1 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/1", sender.textCalls, sender.protoCalls)
	}
	ext := requireExtendedText(t, sender.protoMsg)
	if ext.GetText() != "hello" {
		t.Fatalf("extended text = %q, want hello", ext.GetText())
	}
	if got := ext.GetContextInfo().GetExpiration(); got != defaultEphemeralExpiration {
		t.Fatalf("expiration = %d, want %d", got, defaultEphemeralExpiration)
	}
	if sender.groupInfoCalls != 0 {
		t.Fatalf("GetGroupInfo calls = %d, want 0", sender.groupInfoCalls)
	}
}

func TestSendTextMessageRejectsExplicitZeroDuration(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	sender := &recordingTextSender{}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "hello", "", "", nil, nil, textEphemeralOptions{Duration: "0", DurationSet: true})
	if err == nil || !strings.Contains(err.Error(), "positive duration") {
		t.Fatalf("sendTextMessageWithSender error = %v", err)
	}
	if sender.textCalls != 0 || sender.protoCalls != 0 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/0", sender.textCalls, sender.protoCalls)
	}
	if sender.groupInfoCalls != 0 {
		t.Fatalf("GetGroupInfo calls = %d, want 0", sender.groupInfoCalls)
	}
}

func TestSendTextMessageAppliesEphemeralDuration(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	sender := &recordingTextSender{}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "hello", "", "", nil, nil, textEphemeralOptions{Enabled: true, Duration: "7d"})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	if sender.textCalls != 0 || sender.protoCalls != 1 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/1", sender.textCalls, sender.protoCalls)
	}
	ext := requireExtendedText(t, sender.protoMsg)
	if ext.GetText() != "hello" {
		t.Fatalf("extended text = %q, want hello", ext.GetText())
	}
	if got := ext.GetContextInfo().GetExpiration(); got != 604800 {
		t.Fatalf("expiration = %d, want 604800", got)
	}
	if sender.groupInfoCalls != 0 {
		t.Fatalf("GetGroupInfo calls = %d, want 0", sender.groupInfoCalls)
	}
}

func TestSendTextMessagePreservesExtendedTextWithEphemeralExpiration(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	preview := &linkpreview.Preview{URL: "https://example.com", Title: "Example"}
	sender := &recordingTextSender{}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "hello https://example.com", "", "", preview, nil, textEphemeralOptions{Enabled: true, Duration: "7d"})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	if sender.textCalls != 0 || sender.protoCalls != 1 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/1", sender.textCalls, sender.protoCalls)
	}
	ext := requireExtendedText(t, sender.protoMsg)
	if ext.GetText() != "hello https://example.com" {
		t.Fatalf("extended text = %q", ext.GetText())
	}
	if ext.GetMatchedText() != preview.URL || ext.GetTitle() != preview.Title {
		t.Fatalf("preview fields = (%q, %q), want (%q, %q)", ext.GetMatchedText(), ext.GetTitle(), preview.URL, preview.Title)
	}
	if got := ext.GetContextInfo().GetExpiration(); got != 604800 {
		t.Fatalf("expiration = %d, want 604800", got)
	}
	if sender.groupInfoCalls != 0 {
		t.Fatalf("GetGroupInfo calls = %d, want 0", sender.groupInfoCalls)
	}
}

func TestSendTextMessageUsesGroupEphemeralTimer(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	sender := &recordingTextSender{
		groupInfo: &types.GroupInfo{
			GroupEphemeral: types.GroupEphemeral{
				IsEphemeral:       true,
				DisappearingTimer: 604800,
			},
		},
	}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "hello", "", "", nil, nil, textEphemeralOptions{Enabled: true})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	ext := requireExtendedText(t, sender.protoMsg)
	if got := ext.GetContextInfo().GetExpiration(); got != 604800 {
		t.Fatalf("expiration = %d, want 604800", got)
	}
	if sender.groupInfoCalls != 1 {
		t.Fatalf("GetGroupInfo calls = %d, want 1", sender.groupInfoCalls)
	}
}

func TestSendTextMessageUsesDefaultEphemeralExpirationWhenGroupTimerUnavailable(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	sender := &recordingTextSender{}

	_, err := sendTextMessageWithSender(context.Background(), sender, db, chat, "hello", "", "", nil, nil, textEphemeralOptions{Enabled: true})
	if err != nil {
		t.Fatalf("sendTextMessageWithSender: %v", err)
	}
	if sender.textCalls != 0 || sender.protoCalls != 1 {
		t.Fatalf("calls: SendText=%d SendProtoMessage=%d, want 0/1", sender.textCalls, sender.protoCalls)
	}
	ext := requireExtendedText(t, sender.protoMsg)
	if got := ext.GetContextInfo().GetExpiration(); got != defaultEphemeralExpiration {
		t.Fatalf("expiration = %d, want %d", got, defaultEphemeralExpiration)
	}
	if sender.groupInfoCalls != 1 {
		t.Fatalf("GetGroupInfo calls = %d, want 1", sender.groupInfoCalls)
	}
}

func TestValidateTextEphemeralOptionsRejectsInvalidDuration(t *testing.T) {
	err := validateTextEphemeralOptions(textEphemeralOptions{Duration: "forever", DurationSet: true})
	if err == nil || !strings.Contains(err.Error(), "--ephemeral-duration") {
		t.Fatalf("validateTextEphemeralOptions error = %v", err)
	}
}

func TestValidateTextEphemeralOptionsRejectsZeroDuration(t *testing.T) {
	err := validateTextEphemeralOptions(textEphemeralOptions{Duration: "0", DurationSet: true})
	if err == nil || !strings.Contains(err.Error(), "positive duration") {
		t.Fatalf("validateTextEphemeralOptions error = %v", err)
	}
}

func TestBuildTextMessageCombinesReplyAndMentions(t *testing.T) {
	db := openSendTestDB(t)
	chat := types.JID{User: "12345", Server: types.GroupServer}
	if err := db.UpsertChat(chat.String(), "group", "Group", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "quoted",
		SenderJID: "15557654321@s.whatsapp.net",
		Timestamp: time.Now(),
		Text:      "quoted text",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

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
	if info.GetQuotedMessage().GetConversation() != "quoted text" {
		t.Fatalf("quoted text = %q", info.GetQuotedMessage().GetConversation())
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
