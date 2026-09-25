package app

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// A message that arrives but cannot be decrypted stores no row. Reporting it is
// what separates a chat with a hole in it from a chat that was simply quiet.
func TestSyncEventHandlerWarnsOnUndecryptableMessage(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var eventsOut bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventsOut, true)

	var messagesStored, lastEvent atomic.Int64
	handlerID, _ := a.addSyncEventHandler(
		context.Background(),
		SyncOptions{Mode: SyncModeFollow},
		&messagesStored,
		&lastEvent,
		make(chan struct{}, 1),
		make(chan struct{}, 1),
		make(chan staleReconnectRequest, 1),
		func(string, string) {},
		nil,
		nil,
		&syncPresence{},
		nil,
	)
	defer f.RemoveEventHandler(handlerID)

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	sender := types.JID{User: "456", Server: types.DefaultUserServer}
	f.emit(&events.UndecryptableMessage{
		Info: types.MessageInfo{
			ID:            "lost-1",
			MessageSource: types.MessageSource{Chat: chat, Sender: sender},
		},
		IsUnavailable:   true,
		DecryptFailMode: events.DecryptFailHide,
	})

	evt := findEventByName(t, eventsOut.String(), "warning")
	data, ok := evt["data"].(map[string]any)
	if !ok {
		t.Fatalf("warning event has no data object: %#v", evt)
	}
	if data["code"] != "undecryptable_message" {
		t.Fatalf("warning code = %v, want undecryptable_message", data["code"])
	}
	if data["msg_id"] != "lost-1" {
		t.Fatalf("msg_id = %v, want lost-1", data["msg_id"])
	}
	if data["chat_jid"] != chat.String() || data["sender_jid"] != sender.String() {
		t.Fatalf("event names chat %v and sender %v, want %s and %s", data["chat_jid"], data["sender_jid"], chat, sender)
	}
	if data["is_unavailable"] != true {
		t.Fatalf("is_unavailable = %v, want true", data["is_unavailable"])
	}
	if lastEvent.Load() == 0 {
		t.Fatal("a message that could not be read must still count as activity")
	}
}
