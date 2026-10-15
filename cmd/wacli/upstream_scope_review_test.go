package main

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

type scopeQuotedFileTransport struct {
	*fakeSendFileWA
	quoteID, quoteSender string
}

// Capture the actual constructed document context, so the test distinguishes
// a successfully constructed native quote from omitted local history fields.
func (f *scopeQuotedFileTransport) SendProtoMessage(_ context.Context, _ types.JID, message *waProto.Message) (types.MessageID, error) {
	ctx := message.GetDocumentMessage().GetContextInfo()
	f.quoteID, f.quoteSender = ctx.GetStanzaID(), ctx.GetParticipant()
	return f.sentID, nil
}

// Run the real file-send workflow against its existing fake network transport.
// Assert history immediately, with no sync echo that might later fill it in.
func TestUpstreamScopeSendFileQuotedHistory(t *testing.T) {
	a, db, file := newSendFileFixture(t)
	transport := &scopeQuotedFileTransport{fakeSendFileWA: a.wa.(*fakeSendFileWA)}
	a.wa = transport
	to := types.NewJID("15550000001", types.DefaultUserServer)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(to.String(), "dm", "Fixture", now); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: to.String(), MsgID: "ORIGINAL", SenderJID: to.String(), Timestamp: now, Text: "quoted fixture"}); err != nil {
		t.Fatal(err)
	}
	result, err := sendFile(context.Background(), a, to, file, sendFileOptions{caption: "fixture", replyTo: "ORIGINAL"})
	if err != nil {
		t.Fatal(err)
	}
	if result.id != "MSG1" || result.storeWarning != nil {
		t.Fatalf("expected one accepted, stored send: %+v", result)
	}
	if transport.quoteID != "ORIGINAL" || transport.quoteSender != to.String() {
		t.Fatalf("fixture failed to construct native quote: id=%q sender=%q", transport.quoteID, transport.quoteSender)
	}
	got, err := db.GetMessage(to.String(), "MSG1")
	if err != nil {
		t.Fatal(err)
	}
	if got.QuotedMsgID != transport.quoteID || got.QuotedSenderJID != transport.quoteSender {
		t.Fatalf("immediate outbound quote identity lost: id=%q sender=%q", got.QuotedMsgID, got.QuotedSenderJID)
	}
}
