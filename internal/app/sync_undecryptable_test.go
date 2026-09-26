package app

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// A message that arrives but cannot be decrypted stores no row. Reporting it is
// what separates a chat with a hole in it from a chat that was simply quiet.
// What whatsmeow does next differs per event, so the warning has to as well: it
// must not tell an operator a copy is on its way when none was asked for.
func TestSyncEventHandlerWarnsOnUndecryptableMessage(t *testing.T) {
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	sender := types.JID{User: "456", Server: types.DefaultUserServer}

	group := types.JID{User: "123-456", Server: types.GroupServer}

	tests := []struct {
		name            string
		chat            types.JID
		event           *events.UndecryptableMessage
		wantRecovery    string
		wantUnavailable bool
		wantInMessage   string
		notInMessage    string
	}{
		{
			name: "decryption failed",
			event: &events.UndecryptableMessage{
				DecryptFailMode: events.DecryptFailHide,
			},
			wantRecovery:  "requested_if_possible",
			wantInMessage: "decryption failed, fail mode hide",
			notInMessage:  "primary device",
		},
		{
			name: "no ciphertext for this device",
			event: &events.UndecryptableMessage{
				IsUnavailable: true,
			},
			wantRecovery:    "requested_if_possible",
			wantUnavailable: true,
			wantInMessage:   "nothing readable arrived",
			notInMessage:    "primary device",
		},
		{
			// whatsmeow sets IsUnavailable here too, although ciphertext did
			// arrive and the request to the primary is only the conditional one.
			name: "group message with no sender key",
			chat: group,
			event: &events.UndecryptableMessage{
				IsUnavailable:   true,
				DecryptFailMode: events.DecryptFailShow,
			},
			wantRecovery:    "requested_if_possible",
			wantUnavailable: true,
			wantInMessage:   "a copy is asked back where the failure allows it",
			notInMessage:    "primary device",
		},
		{
			name: "unavailable on purpose",
			event: &events.UndecryptableMessage{
				IsUnavailable:   true,
				UnavailableType: events.UnavailableTypeViewOnce,
			},
			wantRecovery:    "none",
			wantUnavailable: true,
			wantInMessage:   "kept from linked devices on purpose (view_once)",
			notInMessage:    "asked",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

			wantChat := chat
			if !tc.chat.IsEmpty() {
				wantChat = tc.chat
			}
			evtIn := *tc.event
			evtIn.Info = types.MessageInfo{
				ID:            "lost-1",
				MessageSource: types.MessageSource{Chat: wantChat, Sender: sender},
			}
			f.emit(&evtIn)

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
			if data["chat_jid"] != wantChat.String() || data["sender_jid"] != sender.String() {
				t.Fatalf("event names chat %v and sender %v, want %s and %s", data["chat_jid"], data["sender_jid"], wantChat, sender)
			}
			if data["is_unavailable"] != tc.wantUnavailable {
				t.Fatalf("is_unavailable = %v, want %v", data["is_unavailable"], tc.wantUnavailable)
			}
			if data["recovery"] != tc.wantRecovery {
				t.Fatalf("recovery = %v, want %v", data["recovery"], tc.wantRecovery)
			}
			message, _ := data["message"].(string)
			if !strings.Contains(message, tc.wantInMessage) {
				t.Fatalf("message = %q, want it to contain %q", message, tc.wantInMessage)
			}
			if tc.notInMessage != "" && strings.Contains(message, tc.notInMessage) {
				t.Fatalf("message = %q, want it not to contain %q", message, tc.notInMessage)
			}
			if lastEvent.Load() == 0 {
				t.Fatal("a message that could not be read must still count as activity")
			}
		})
	}
}
