package app

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSecretEditRejectsContentWrappedMutation(t *testing.T) {
	for name, wrap := range map[string]func(*waE2E.Message) *waE2E.Message{
		"associated-child": func(m *waE2E.Message) *waE2E.Message {
			return &waE2E.Message{AssociatedChildMessage: &waE2E.FutureProofMessage{Message: m}}
		},
		"group-status": func(m *waE2E.Message) *waE2E.Message {
			return &waE2E.Message{GroupStatusMentionMessage: &waE2E.FutureProofMessage{Message: m}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			a := newTestApp(t)
			f := newCryptoEditWA(t)
			a.wa = f
			chat := types.JID{User: "120363000000", Server: types.GroupServer}
			sender := types.JID{User: "15550000001", Server: types.DefaultUserServer}
			secret := bytes.Repeat([]byte{0x23}, 32)
			if err := f.client.Store.MsgSecrets.PutMessageSecret(ctx, chat, sender, "original-id", secret); err != nil {
				t.Fatal(err)
			}
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			original := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: true}, ID: "original-id", Timestamp: base}, Message: &waE2E.Message{Conversation: proto.String("original body")}}
			var stored atomic.Int64
			a.handleLiveSyncMessage(ctx, SyncOptions{}, original, &stored, func(string, string) {}, nil)
			evt := &events.Message{Info: original.Info, Message: secretGroupEditEnvelope(chat, sender, "original-id")}
			evt.Info.ID = "edit-envelope"
			evt.Info.Timestamp = base.Add(time.Minute)
			payload := decryptedProtocolEdit("outer body")
			payload.ProtocolMessage.EditedMessage = wrap(decryptedProtocolEdit("nested body"))
			encryptEditFixture(t, evt, sender, secret, payload)
			webhooks := 0
			a.handleLiveSyncMessage(ctx, SyncOptions{}, evt, &stored, func(string, string) {}, func(wa.ParsedMessage) { webhooks++ })
			msg, err := a.db.GetMessage(chat.String(), "original-id")
			if err != nil {
				t.Fatal(err)
			}
			if msg.Text != "original body" || msg.Edited || stored.Load() != 1 || webhooks != 0 {
				t.Fatalf("nested edit accepted: %+v; stored=%d webhooks=%d", msg, stored.Load(), webhooks)
			}
		})
	}
}
