package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestHistorySyncBoundsUnhandledPayloadWarnings(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	var eventsOut bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventsOut, true)
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	const messageCount = 25
	messages := make([]*waHistorySync.HistorySyncMsg, 0, messageCount)
	for i := range messageCount {
		messages = append(messages, &waHistorySync.HistorySyncMsg{
			Message: &waWeb.WebMessageInfo{
				Key: &waCommon.MessageKey{
					RemoteJID: proto.String(chat.String()),
					ID:        proto.String(fmt.Sprintf("opaque-%d", i)),
				},
				MessageTimestamp: proto.Uint64(uint64(time.Unix(int64(i+1), 0).Unix())),
				Message: &waProto.Message{StickerSyncRmrMessage: &waProto.StickerSyncRMRMessage{
					Filehash: []string{"abc"},
				}},
			},
		})
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       proto.String(chat.String()),
			Messages: messages,
		}},
	}}

	var stored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(
		context.Background(), SyncOptions{}, history, &stored, &lastEvent,
		func(string, string) {},
	)

	if got := stored.Load(); got != messageCount {
		t.Fatalf("stored messages = %d, want %d", got, messageCount)
	}
	var detailed, summaries int
	for _, line := range strings.Split(strings.TrimSpace(eventsOut.String()), "\n") {
		var event struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		if event.Event != "warning" {
			continue
		}
		switch event.Data["code"] {
		case "unhandled_message_payload":
			detailed++
		case "unhandled_message_payloads_suppressed":
			summaries++
			if got := event.Data["suppressed_messages"]; got != float64(messageCount-1) {
				t.Fatalf("suppressed_messages = %#v, want %d", got, messageCount-1)
			}
		}
	}
	if detailed != 1 || summaries != 1 {
		t.Fatalf("warning events: detailed=%d summaries=%d, want 1 each\n%s", detailed, summaries, eventsOut.String())
	}
}

func TestHistoryUnhandledPayloadWarningsCapsDistinctShapes(t *testing.T) {
	a := newTestApp(t)
	var eventsOut bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventsOut, true)

	var warnings historyUnhandledPayloadWarnings
	for i := range maxHistoryUnhandledPayloadWarnings + 5 {
		warnings.observe(a, wa.ParsedMessage{
			ID:               fmt.Sprintf("opaque-%d", i),
			UnhandledPayload: fmt.Sprintf("payload-%d", i),
		})
	}
	warnings.flush(a)

	if got := strings.Count(eventsOut.String(), `"code":"unhandled_message_payload"`); got != maxHistoryUnhandledPayloadWarnings {
		t.Fatalf("detailed warnings = %d, want %d", got, maxHistoryUnhandledPayloadWarnings)
	}
	if !strings.Contains(eventsOut.String(), `"suppressed_messages":5`) {
		t.Fatalf("missing suppression summary:\n%s", eventsOut.String())
	}
}
