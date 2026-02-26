package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/out"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSyncEmitsJSONEvents(t *testing.T) {
	var buf bytes.Buffer
	a := newTestApp(t)
	a.events = out.NewEventWriter(&buf, true)

	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{
		Found:    true,
		FullName: "Alice",
		PushName: "Alice",
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Create a history sync event with 2 conversations
	histMsg1 := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("m-hist-1"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello")},
	}
	history := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_FULL.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:       proto.String(chat.String()),
				Messages: []*waHistorySync.HistorySyncMsg{{Message: histMsg1}},
			}},
		},
	}

	f.connectEvents = []interface{}{history}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, err := a.Sync(ctx, SyncOptions{
		Mode:    SyncModeFollow,
		AllowQR: false,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Parse all emitted events
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	eventNames := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ev out.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal event line %q: %v", line, err)
		}
		eventNames = append(eventNames, ev.Event)
		if ev.TS <= 0 {
			t.Fatalf("expected positive timestamp in event %q", ev.Event)
		}
	}

	// Should contain: connected, history_sync, progress, stopping
	assertContains(t, eventNames, "connected")
	assertContains(t, eventNames, "history_sync")
	assertContains(t, eventNames, "progress")
	assertContains(t, eventNames, "stopping")
}

func TestSyncEventsDisabledNoJSON(t *testing.T) {
	var buf bytes.Buffer
	a := newTestApp(t)
	// Events disabled (default)
	a.events = out.NewEventWriter(&buf, false)

	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := a.Sync(ctx, SyncOptions{
		Mode:    SyncModeFollow,
		AllowQR: false,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Should have no JSON output in our buffer
	if buf.Len() != 0 {
		t.Fatalf("expected no event output when disabled, got %q", buf.String())
	}
}

func TestSyncIdleExitEmitsEvent(t *testing.T) {
	var buf bytes.Buffer
	a := newTestApp(t)
	a.events = out.NewEventWriter(&buf, true)

	f := newFakeWA()
	a.wa = f

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	eventNames := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ev out.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal event line %q: %v", line, err)
		}
		eventNames = append(eventNames, ev.Event)
	}

	assertContains(t, eventNames, "idle_exit")
}

func assertContains(t *testing.T, slice []string, want string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Fatalf("expected %q in %v", want, slice)
}
