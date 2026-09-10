package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// An incoming edit arrives as a SecretEncryptedMessage with
// SecretEncType MESSAGE_EDIT and a TargetMessageKey pointing at the original,
// not as a bare ProtocolMessage. Only POLL_ADD_OPTION is decrypted today, so
// the edit is stored as a second empty row and the original keeps its
// superseded text: the reader sees stale content that looks valid.
func TestLiveSyncAppliesSecretEncryptedMessageEdit(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "34600000000", Server: types.DefaultUserServer}
	originalID := "ORIGINAL-1"
	sent := time.Date(2026, 9, 10, 12, 29, 56, 0, time.UTC)
	var stored atomic.Int64

	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            originalID,
			Timestamp:     sent,
		},
		Message: &waProto.Message{Conversation: proto.String("Uno")},
	}, &stored, func(string, string) {}, nil)

	// What the server actually delivers for an edit.
	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key: &waCommon.MessageKey{
					ID:        proto.String(originalID),
					RemoteJID: proto.String(chat.String()),
				},
				EditedMessage: &waE2E.Message{Conversation: proto.String("Dos editado")},
			},
		}, nil
	}

	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "EDIT-ENVELOPE-1",
			Timestamp:     sent.Add(15 * time.Second),
		},
		Message: &waProto.Message{
			SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
				TargetMessageKey: &waCommon.MessageKey{
					ID:        proto.String(originalID),
					RemoteJID: proto.String(chat.String()),
				},
				SecretEncType: waE2E.SecretEncryptedMessage_MESSAGE_EDIT.Enum(),
			},
		},
	}, &stored, func(string, string) {}, nil)

	msgs, err := a.db.ListMessages(store.ListMessagesParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	var original *store.Message
	for i := range msgs {
		if msgs[i].MsgID == originalID {
			original = &msgs[i]
		}
	}
	if original == nil {
		t.Fatalf("original row missing; got %d rows", len(msgs))
	}

	// The point of the bug: the reader gets the pre-edit text, which reads as
	// valid, rather than a visible gap.
	if original.Text != "Dos editado" {
		t.Errorf("original text = %q, want %q (edit not applied)", original.Text, "Dos editado")
	}
	if !original.Edited {
		t.Error("original.Edited = false, want true")
	}

	// And the envelope must not survive as a separate content-less row.
	for _, m := range msgs {
		if m.MsgID == "EDIT-ENVELOPE-1" {
			t.Errorf("envelope stored as its own row (text=%q display=%q)", m.Text, m.DisplayText)
		}
	}
}

// helper: the envelope the server sends for an edit.
func secretEditEvent(chat types.JID, envelopeID, targetID string, ts time.Time, t waE2E.SecretEncryptedMessage_SecretEncType) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            envelopeID,
			Timestamp:     ts,
		},
		Message: &waProto.Message{
			SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
				TargetMessageKey: &waCommon.MessageKey{
					ID:        proto.String(targetID),
					RemoteJID: proto.String(chat.String()),
				},
				SecretEncType: t.Enum(),
			},
		},
	}
}

// Messages can arrive out of order, so the edit may land before the message it
// edits. The original must not then overwrite the newer body with its own.
func TestSecretEncryptedEditSurvivesLateOriginal(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "34600000001", Server: types.DefaultUserServer}
	originalID := "LATE-ORIGINAL"
	sent := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	var stored atomic.Int64

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key:           &waCommon.MessageKey{ID: proto.String(originalID), RemoteJID: proto.String(chat.String())},
				EditedMessage: &waE2E.Message{Conversation: proto.String("nuevo")},
			},
		}, nil
	}
	// Edit first.
	a.handleLiveSyncMessage(context.Background(), SyncOptions{},
		secretEditEvent(chat, "ENV-LATE", originalID, sent.Add(time.Minute), waE2E.SecretEncryptedMessage_MESSAGE_EDIT),
		&stored, func(string, string) {}, nil)
	// Original after.
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: originalID, Timestamp: sent},
		Message: &waProto.Message{Conversation: proto.String("viejo")},
	}, &stored, func(string, string) {}, nil)

	msgs, err := a.db.ListMessages(store.ListMessagesParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.MsgID == originalID && m.Text != "nuevo" {
			t.Fatalf("late original overwrote the edit: text = %q", m.Text)
		}
	}
}

// Only MESSAGE_EDIT envelopes belong to this path. Treating every
// SecretEncryptedMessage as an edit would hijack poll and event traffic.
func TestSecretEncryptedEditIgnoresOtherEncTypes(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "34600000002", Server: types.DefaultUserServer}
	var stored atomic.Int64
	called := false
	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		called = true
		return &waE2E.Message{Conversation: proto.String("no deberia usarse")}, nil
	}

	for _, enc := range []waE2E.SecretEncryptedMessage_SecretEncType{
		waE2E.SecretEncryptedMessage_POLL_EDIT,
		waE2E.SecretEncryptedMessage_EVENT_EDIT,
		waE2E.SecretEncryptedMessage_MESSAGE_SCHEDULE,
	} {
		called = false
		a.handleLiveSyncMessage(context.Background(), SyncOptions{},
			secretEditEvent(chat, "ENV-"+enc.String(), "TARGET", time.Now().UTC(), enc),
			&stored, func(string, string) {}, nil)
		if called {
			t.Errorf("%s was routed through the edit path", enc)
		}
	}
}

// A failed decrypt must warn and leave the message alone, not abort the sync or
// invent content.
func TestSecretEncryptedEditSurvivesDecryptFailure(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "34600000003", Server: types.DefaultUserServer}
	var stored atomic.Int64
	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return nil, context.DeadlineExceeded
	}

	a.handleLiveSyncMessage(context.Background(), SyncOptions{},
		secretEditEvent(chat, "ENV-FAIL", "TARGET-FAIL", time.Now().UTC(), waE2E.SecretEncryptedMessage_MESSAGE_EDIT),
		&stored, func(string, string) {}, nil)

	msgs, err := a.db.ListMessages(store.ListMessagesParams{ChatJID: chat.String(), Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, m := range msgs {
		if m.Text != "" {
			t.Fatalf("invented content on decrypt failure: %q", m.Text)
		}
	}
}
