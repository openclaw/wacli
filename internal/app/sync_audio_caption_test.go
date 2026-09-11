package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSyncAudioCaptionRoundTrip(t *testing.T) {
	for _, mode := range []string{"live", "history"} {
		for _, supplied := range []string{"", "recording from this morning", "[Audio]"} {
			t.Run(mode+"/"+supplied, func(t *testing.T) {
				a := newTestApp(t)
				a.wa = newFakeWA()
				chat := types.JID{User: "15555550123", Server: types.DefaultUserServer}
				stamp := time.Unix(1700000000, 0)
				const id = "AUDIO-FIXTURE"
				if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", stamp); err != nil {
					t.Fatal(err)
				}
				// Re-ingestion must correct an ordinary legacy row as well as new messages.
				if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, Timestamp: stamp, Text: "[Audio]", MediaType: "audio", MediaCaption: "[Audio]"}); err != nil {
					t.Fatal(err)
				}
				payload := &waProto.Message{Conversation: proto.String(supplied), AudioMessage: &waProto.AudioMessage{Mimetype: proto.String("audio/ogg"), PTT: proto.Bool(true)}}
				var stored, last atomic.Int64
				if mode == "live" {
					evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: id, Timestamp: stamp}, Message: payload}
					a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &stored, nil, nil)
				} else {
					msg := &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String(id), RemoteJID: proto.String(chat.String())}, MessageTimestamp: proto.Uint64(uint64(stamp.Unix())), Message: payload}
					hist := &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{ID: proto.String(chat.String()), Messages: []*waHistorySync.HistorySyncMsg{{Message: msg}}}}}}
					a.handleHistorySync(context.Background(), SyncOptions{}, hist, &stored, &last, nil)
				}
				if stored.Load() != 1 {
					t.Fatalf("stored %d messages, want 1", stored.Load())
				}
				got, err := a.db.GetMessage(chat.String(), id)
				if err != nil {
					t.Fatal(err)
				}
				text := supplied
				if text == "" {
					text = "[Audio]"
				}
				if got.Text != text || got.MediaCaption != supplied || got.DisplayText != "Sent audio" {
					t.Fatalf("unexpected audio row: text=%q caption=%q display=%q", got.Text, got.MediaCaption, got.DisplayText)
				}
			})
		}
	}
}
