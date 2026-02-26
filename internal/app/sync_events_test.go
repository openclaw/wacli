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

func TestSyncEmitsNewMessageEvent(t *testing.T) {
	var buf bytes.Buffer
	a := newTestApp(t)
	a.events = out.NewEventWriter(&buf, true)

	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	sender := types.JID{User: "456", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{
		Found:    true,
		FullName: "Alice",
		PushName: "Alice",
	}

	base := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	// A live text message arriving after connect
	liveMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "msg-live-1",
			Timestamp: base,
			PushName:  "Bob",
		},
		Message: &waProto.Message{Conversation: proto.String("hey are you free?")},
	}

	f.connectEvents = []interface{}{liveMsg}

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

	// Parse all emitted events, find new_message
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var newMsgEvent *out.Event
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ev out.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal event line %q: %v", line, err)
		}
		if ev.Event == "new_message" {
			newMsgEvent = &ev
		}
	}

	if newMsgEvent == nil {
		t.Fatalf("expected new_message event, got events: %s", buf.String())
	}

	// Verify data fields
	data, ok := newMsgEvent.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be map, got %T", newMsgEvent.Data)
	}

	if id, _ := data["id"].(string); id != "msg-live-1" {
		t.Errorf("expected id=msg-live-1, got %q", id)
	}
	if chat, _ := data["chat"].(string); chat != "123@s.whatsapp.net" {
		t.Errorf("expected chat=123@s.whatsapp.net, got %q", chat)
	}
	if text, _ := data["text"].(string); text != "hey are you free?" {
		t.Errorf("expected text='hey are you free?', got %q", text)
	}
	if pushName, _ := data["push_name"].(string); pushName != "Bob" {
		t.Errorf("expected push_name=Bob, got %q", pushName)
	}
	if fromMe, _ := data["from_me"].(bool); fromMe {
		t.Error("expected from_me=false")
	}
	if isGroup, _ := data["is_group"].(bool); isGroup {
		t.Error("expected is_group=false")
	}
}

func TestSyncEmitsNewMessageEventForGroup(t *testing.T) {
	var buf bytes.Buffer
	a := newTestApp(t)
	a.events = out.NewEventWriter(&buf, true)

	f := newFakeWA()
	a.wa = f

	groupChat := types.JID{User: "999", Server: types.GroupServer}
	sender := types.JID{User: "456", Server: types.DefaultUserServer}

	liveMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     groupChat,
				Sender:   sender,
				IsFromMe: false,
			},
			ID:        "msg-group-1",
			Timestamp: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			PushName:  "Charlie",
		},
		Message: &waProto.Message{Conversation: proto.String("group message")},
	}

	f.connectEvents = []interface{}{liveMsg}

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

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ev out.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ev.Event == "new_message" {
			data, _ := ev.Data.(map[string]interface{})
			if isGroup, _ := data["is_group"].(bool); !isGroup {
				t.Error("expected is_group=true for group message")
			}
			if chat, _ := data["chat"].(string); chat != "999@g.us" {
				t.Errorf("expected chat=999@g.us, got %q", chat)
			}
			return
		}
	}
	t.Fatal("expected new_message event for group message")
}

func TestSyncNewMessageNotEmittedWhenEventsDisabled(t *testing.T) {
	var buf bytes.Buffer
	a := newTestApp(t)
	a.events = out.NewEventWriter(&buf, false) // disabled

	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{Found: true, PushName: "Alice"}

	liveMsg := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     chat,
				Sender:   chat,
				IsFromMe: false,
			},
			ID:        "msg-no-event",
			Timestamp: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			PushName:  "Alice",
		},
		Message: &waProto.Message{Conversation: proto.String("should not emit")},
	}

	f.connectEvents = []interface{}{liveMsg}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, _ = a.Sync(ctx, SyncOptions{Mode: SyncModeFollow, AllowQR: false})

	if buf.Len() != 0 {
		t.Fatalf("expected no event output when disabled, got %q", buf.String())
	}
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
