package main

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Restrict the mapping to the exact known alias, as the real resolver does.
// The same recording transport supports both text and file send workflows.
type canonicalQuoteWA struct {
	*historyTextWA
	lid, pn types.JID
}

func TestPersistQuoteIdentityLeavesNativeContextUntouched(t *testing.T) {
	db := openSendTestDB(t)
	lid := types.NewJID("20001", types.HiddenUserServer)
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	resolver := outboundTextResolverStub{lid: lid, pn: pn}
	quote := &waProto.ContextInfo{
		StanzaID: proto.String("ORIGINAL"), Participant: proto.String(lid.String()),
		IsForwarded: proto.Bool(true), ForwardingScore: proto.Uint32(3),
		MentionedJID:  []string{lid.String()},
		QuotedMessage: &waProto.Message{Conversation: proto.String("original")},
	}
	before := proto.Clone(quote)
	if err := persistOutboundTextWith(context.Background(), db, resolver, pn, "accepted", "reply", time.Now(), quote); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(quote, before) {
		t.Fatal("persisting canonical history mutated the native context")
	}
	stored, err := db.GetMessage(pn.String(), "accepted")
	if err != nil || stored.QuotedSenderJID != pn.String() {
		t.Fatalf("stored sender=%q err=%v", stored.QuotedSenderJID, err)
	}
}

func (w *canonicalQuoteWA) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	if jid == w.lid && !w.pn.IsEmpty() {
		return w.pn
	}
	return jid
}

func (w *canonicalQuoteWA) Upload(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, nil
}

func TestSendQuoteHistoryCanonicalizesKnownSenderWithoutChangingWire(t *testing.T) {
	group := types.NewJID("12345-123", types.GroupServer)
	lid := types.NewJID("20001", types.HiddenUserServer)
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	for _, kind := range []string{"text", "file"} {
		for _, mapped := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/unknown", true: "/mapped"}[mapped], func(t *testing.T) {
				a, db, filePath := newSendFileFixture(t)
				sender := &recordingTextSender{}
				transport := &canonicalQuoteWA{historyTextWA: &historyTextWA{sender: sender}, lid: lid}
				want := lid.String()
				if mapped {
					transport.pn = pn
					want = pn.String()
				}
				a.wa = transport
				var id types.MessageID
				if kind == "text" {
					accepted, warning, err := sendTextWithHistory(context.Background(), a, nil, group, "reply", "ORIGINAL", lid.String(), nil, nil, textEphemeralOptions{}, textSendOptions{})
					if err != nil || warning != nil {
						t.Fatalf("send error=%v warning=%v", err, warning)
					}
					id = accepted
				} else {
					outcome, err := sendFile(context.Background(), a, group, filePath, sendFileOptions{replyTo: "ORIGINAL", replyToSender: lid.String()})
					if err != nil || outcome.storeWarning != nil {
						t.Fatalf("send error=%v warning=%v", err, outcome.storeWarning)
					}
					id = outcome.id
				}
				if sender.protoCalls != 1 || sender.protoRecipient != group {
					t.Fatalf("unexpected dispatch: calls=%d recipient=%s", sender.protoCalls, sender.protoRecipient)
				}
				wire := sender.protoMsg.GetExtendedTextMessage().GetContextInfo()
				if kind == "file" {
					wire = sender.protoMsg.GetDocumentMessage().GetContextInfo()
				}
				if wire.GetParticipant() != lid.String() || wire.GetStanzaID() != "ORIGINAL" {
					t.Fatalf("native quote changed: %v", wire)
				}
				stored, err := db.GetMessage(group.String(), string(id))
				if err != nil || stored.QuotedSenderJID != want || stored.QuotedMsgID != wire.GetStanzaID() {
					t.Fatalf("history quote=%q/%q want ORIGINAL/%q, err=%v", stored.QuotedMsgID, stored.QuotedSenderJID, want, err)
				}
			})
		}
	}
}
