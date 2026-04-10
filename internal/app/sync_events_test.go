package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/out"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSyncEmitsLifecycleEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var buf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&buf, true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	lines := splitEventLines(t, buf.String())
	assertEventSeen(t, lines, "connected")
	assertEventSeen(t, lines, "idle_exit")
}

func TestBackfillEmitsLifecycleEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	var buf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&buf, true)

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	chatStr := chat.String()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := a.db.UpsertChat(chatStr, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(chatStr, "m2", base.Add(2*time.Second), "newer")); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	f.onDemandHistory = func(lastKnown types.MessageInfo, count int) *events.HistorySync {
		return &events.HistorySync{
			Data: &waHistorySync.HistorySync{
				SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
				Conversations: []*waHistorySync.Conversation{{
					ID: proto.String("999@s.whatsapp.net"),
				}},
			},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := a.BackfillHistory(ctx, BackfillOptions{
		ChatJID:        chatStr,
		Count:          10,
		Requests:       1,
		WaitPerRequest: 50 * time.Millisecond,
		IdleExit:       20 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("expected backfill to fail without a response")
	}

	lines := splitEventLines(t, buf.String())
	assertEventSeen(t, lines, "backfill_requesting")
}

func splitEventLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatalf("unmarshal line %q: %v", line, err)
		}
		lines = append(lines, evt)
	}
	return lines
}

func assertEventSeen(t *testing.T, lines []map[string]any, want string) {
	t.Helper()
	for _, line := range lines {
		if line["event"] == want {
			return
		}
	}
	t.Fatalf("event %q not found in %#v", want, lines)
}
