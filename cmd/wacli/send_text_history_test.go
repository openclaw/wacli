package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// Use the native-message recording transport through the same send-and-save
// workflow called by both the direct command and the socket delegate.
type historyTextWA struct {
	app.WAClient
	sender    *recordingTextSender
	afterSend func()
}

func (w *historyTextWA) SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error) {
	id, err := w.sender.SendText(ctx, to, text)
	if w.afterSend != nil {
		w.afterSend()
	}
	return id, err
}
func (w *historyTextWA) SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	id, err := w.sender.SendProtoMessage(ctx, to, msg)
	if w.afterSend != nil {
		w.afterSend()
	}
	return id, err
}
func (w *historyTextWA) ResolveChatName(context.Context, types.JID, string) string { return "Fixture" }
func (w *historyTextWA) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	// Model self and recipient mappings independently; they are distinct people.
	if jid == w.sender.linkedLID && w.sender.linkedJID != "" {
		self, _ := types.ParseJID(w.sender.linkedJID)
		return self
	}
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	return w.sender.ResolveLIDToPN(ctx, jid)
}
func (w *historyTextWA) ResolvePNToLID(ctx context.Context, jid types.JID) types.JID {
	return w.sender.ResolvePNToLID(ctx, jid)
}
func (w *historyTextWA) LinkedJID() string { return w.sender.LinkedJID() }
func (w *historyTextWA) LinkedLID() string { return w.sender.LinkedLID() }
func (w *historyTextWA) GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error) {
	return w.sender.GetGroupInfo(ctx, jid)
}

func TestSendTextImmediateHistoryMatchesNativeQuote(t *testing.T) {
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("20001", types.HiddenUserServer)
	self := types.NewJID("15550000009", types.DefaultUserServer)
	selfLID := types.NewJID("20009", types.HiddenUserServer)
	group := types.NewJID("12345-123", types.GroupServer)
	for _, tc := range []struct {
		name                  string
		to, storedChat        types.JID
		sender                string
		own, noQuote, missing bool
		override, wantSender  string
		wantWireSender        string
	}{
		{name: "incoming inferred", to: pn, storedChat: pn, sender: pn.String(), wantSender: pn.String()},
		{name: "incoming missing sender", to: pn, storedChat: pn, wantSender: pn.String()},
		{name: "own empty stored sender", to: pn, storedChat: pn, own: true, wantSender: self.String()},
		{name: "own alias uses linked LID", to: lid, storedChat: pn, own: true, wantSender: self.String(), wantWireSender: selfLID.String()},
		{name: "incoming alias retains sender", to: lid, storedChat: pn, sender: pn.String(), wantSender: pn.String()},
		{name: "unsynced group normalizes explicit sender", to: group, missing: true, override: "15550000001", wantSender: pn.String()},
		{name: "no quote", to: pn, noQuote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openSendTestDB(t)
			sender := &recordingTextSender{linkedJID: self.String(), linkedLID: selfLID}
			if tc.to == lid {
				sender.lidToPN = pn
			}
			transport := &historyTextWA{sender: sender}
			a := &fakeSendFileApp{wa: transport, db: db}
			if !tc.missing && !tc.noQuote {
				if err := db.UpsertChat(tc.storedChat.String(), "dm", "Fixture", time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: tc.storedChat.String(), MsgID: "ORIGINAL", SenderJID: tc.sender, FromMe: tc.own, Timestamp: time.Now(), Text: "Original"}); err != nil {
					t.Fatal(err)
				}
			}
			quote := "  ORIGINAL  "
			if tc.noQuote {
				quote = ""
			}
			id, warning, err := sendTextWithHistory(context.Background(), a, func(context.Context) error { return fmt.Errorf("unexpected reconnect") }, tc.to, "reply", quote, tc.override, nil, nil, textEphemeralOptions{}, textSendOptions{})
			if err != nil || warning != nil {
				t.Fatalf("send=%v warning=%v", err, warning)
			}
			if sender.textCalls+sender.protoCalls != 1 {
				t.Fatal("expected exactly one send")
			}
			got, err := db.GetMessage(canonicalOutboundChat(context.Background(), transport, tc.to).String(), string(id))
			if err != nil {
				t.Fatal(err)
			}
			native := sender.protoMsg.GetExtendedTextMessage().GetContextInfo()
			wantWire := tc.wantWireSender
			if wantWire == "" {
				wantWire = tc.wantSender
			}
			if got.QuotedMsgID != native.GetStanzaID() || native.GetParticipant() != wantWire {
				t.Fatalf("history quote ID %q, native %q/%q want sender %q", got.QuotedMsgID, native.GetStanzaID(), native.GetParticipant(), wantWire)
			}
			if got.QuotedSenderJID != tc.wantSender {
				t.Fatalf("sender=%q want %q", got.QuotedSenderJID, tc.wantSender)
			}
			if !tc.noQuote && got.QuotedMsgID != "ORIGINAL" {
				t.Fatalf("quote ID=%q", got.QuotedMsgID)
			}
		})
	}
}

func TestSendTextHistoryRejectsQuoteBeforeSending(t *testing.T) {
	db := openSendTestDB(t)
	sender := &recordingTextSender{}
	a := &fakeSendFileApp{wa: &historyTextWA{sender: sender}, db: db}
	to := types.NewJID("15550000001", types.DefaultUserServer)
	id, warning, err := sendTextWithHistory(context.Background(), a, nil, to, "reply", "missing", "", nil, nil, textEphemeralOptions{}, textSendOptions{})
	if err == nil || warning != nil || id != "" {
		t.Fatalf("id=%q warning=%v error=%v", id, warning, err)
	}
	if sender.textCalls+sender.protoCalls != 0 {
		t.Fatal("invalid quote was sent")
	}
	if _, err := db.GetMessage(to.String(), "proto-id"); err == nil {
		t.Fatal("invalid quote was persisted")
	}
}

func TestSendTextHistorySaveFailureDoesNotResend(t *testing.T) {
	db := openSendTestDB(t)
	sender := &recordingTextSender{}
	to := types.NewJID("15550000001", types.DefaultUserServer)
	if err := db.UpsertChat(to.String(), "dm", "Fixture", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: to.String(), MsgID: "ORIGINAL", SenderJID: to.String(), Timestamp: time.Now(), Text: "Original"}); err != nil {
		t.Fatal(err)
	}
	transport := &historyTextWA{sender: sender, afterSend: func() { _ = db.Close() }}
	a := &fakeSendFileApp{wa: transport, db: db}
	reconnects := 0
	id, warning, err := sendTextWithHistory(context.Background(), a, func(context.Context) error { reconnects++; return nil }, to, "reply", "ORIGINAL", "", nil, nil, textEphemeralOptions{}, textSendOptions{})
	if err != nil || warning == nil || id != "proto-id" {
		t.Fatalf("id=%q warning=%v error=%v", id, warning, err)
	}
	if sender.protoCalls != 1 || reconnects != 0 {
		t.Fatalf("send calls=%d reconnects=%d", sender.protoCalls, reconnects)
	}
}
