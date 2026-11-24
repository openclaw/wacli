package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestLiveSyncSkipsMetaAIMetadataOperation(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	bot := types.JID{User: "123", Server: types.BotServer}
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: bot, Sender: bot},
			ID:            "metadata-event",
			Timestamp:     time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		},
		Message: &waProto.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_AI_METADATA_OPERATION.Enum(),
		}},
	}, &messagesStored, func(string, string) {}, nil)

	if n, err := a.db.CountMessages(); err != nil || n != 0 {
		t.Fatalf("expected no stored rows for a Meta AI metadata operation, got %d (err=%v)", n, err)
	}
}
