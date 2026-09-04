package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func fixedOffsetZone() *time.Location { return time.FixedZone("test-0400", -4*60*60) }

// docs/sync.md documents `Z` for every webhook Timestamp, and the store and CLI
// already emit it. The payload is where a host-local value would otherwise reach
// a consumer, so pin the serialized bytes rather than the Go value.
func TestWebhookMessagePayloadSerializesTimestampAsUTC(t *testing.T) {
	a := newTestApp(t)
	local := time.Date(2026, 9, 4, 14, 57, 52, 0, fixedOffsetZone())
	chat, _ := types.ParseJID("15551234567@s.whatsapp.net")

	pm := wa.ParseLiveMessage(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat},
			ID:            "MSGID",
			Timestamp:     local,
		},
		Message: &waProto.Message{Conversation: proto.String("hi")},
	})

	body, err := json.Marshal(a.newSyncWebhookPayload(context.Background(), pm))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"Timestamp":"2026-09-04T18:57:52Z"`) {
		t.Fatalf("message payload timestamp is not UTC: %s", body)
	}
}

// Receipts carry the raw event time and are the other payload kind the docs
// show with a `Z`.
func TestWebhookReceiptPayloadSerializesTimestampAsUTC(t *testing.T) {
	a := newTestApp(t)
	local := time.Date(2026, 9, 4, 14, 57, 53, 0, fixedOffsetZone())
	chat, _ := types.ParseJID("120363000000000000@g.us")
	sender, _ := types.ParseJID("15551234567@s.whatsapp.net")

	evt, ok := newSyncWebhookReceiptEvent(&events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender},
		MessageIDs:    []types.MessageID{"MSGID"},
		Type:          types.ReceiptTypeDelivered,
		Timestamp:     local,
	})
	if !ok {
		t.Fatal("expected a receipt webhook event")
	}

	body, err := json.Marshal(a.newSyncWebhookEventPayload(context.Background(), evt))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"Timestamp":"2026-09-04T18:57:53Z"`) {
		t.Fatalf("receipt payload timestamp is not UTC: %s", body)
	}
}
