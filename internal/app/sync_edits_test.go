package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveSyncDecryptsSecretMessageEditBeforeStorage(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("edited body"), nil
	}
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "edit-event",
			Timestamp:     base.Add(time.Minute),
		},
		Message: secretEditEnvelope(chat, "original-id"),
	}, &messagesStored, func(string, string) {}, nil)

	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage edited original: %v", err)
	}
	if msg.Text != "edited body" || !msg.Edited {
		t.Fatalf("encrypted edit was not applied: %+v", msg)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 1 {
		t.Fatalf("expected only the original row, got %d (err=%v)", n, err)
	}
}

func TestLiveSyncRejectsSecretMessageEditTargetMismatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		decrypted := decryptedProtocolEdit("wrong body")
		decrypted.ProtocolMessage.Key = &waCommon.MessageKey{ID: proto.String("other-id")}
		return decrypted, nil
	}
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat},
				ID:            "edit-event",
				Timestamp:     base.Add(time.Minute),
			},
			Message: secretEditEnvelope(chat, "original-id"),
		}, &messagesStored, func(string, string) {}, nil)
	})

	if !strings.Contains(out, "target does not match decrypted payload") {
		t.Fatalf("expected target mismatch warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited {
		t.Fatalf("mismatched edit changed original: %+v", msg)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 1 {
		t.Fatalf("expected only the original row, got %d (err=%v)", n, err)
	}
}

func TestLiveSyncRejectsSecretMessageEditFromDifferentGroupParticipant(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	group := types.JID{User: "120363000000", Server: types.GroupServer}
	originalSender := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	editor := types.JID{User: "15550000002", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: group, Sender: originalSender, IsGroup: true},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("unauthorized body"), nil
	}
	webhooks := 0
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: group, Sender: editor, IsGroup: true},
				ID:            "edit-event",
				Timestamp:     base.Add(time.Minute),
			},
			Message: secretGroupEditEnvelope(group, originalSender, "original-id"),
		}, &messagesStored, func(string, string) {}, func(wa.ParsedMessage) { webhooks++ })
	})

	if !strings.Contains(out, "sender does not own the target message") {
		t.Fatalf("expected sender mismatch warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(group.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited {
		t.Fatalf("unauthorized edit changed original: %+v", msg)
	}
	if webhooks != 0 {
		t.Fatalf("unauthorized edit published %d webhooks", webhooks)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 1 {
		t.Fatalf("expected only the original row, got %d (err=%v)", n, err)
	}
}

func TestLiveSyncAcceptsSecretMessageEditAcrossLIDAlias(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	group := types.JID{User: "120363000000", Server: types.GroupServer}
	senderLID := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	senderPN := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[senderLID] = senderPN
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: group, Sender: senderPN, IsGroup: true},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("edited body"), nil
	}
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: group, Sender: senderLID, IsGroup: true},
			ID:            "edit-event",
			Timestamp:     base.Add(time.Minute),
		},
		Message: secretGroupEditEnvelope(group, senderPN, "original-id"),
	}, &messagesStored, func(string, string) {}, nil)

	msg, err := a.db.GetMessage(group.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage edited original: %v", err)
	}
	if msg.Text != "edited body" || !msg.Edited {
		t.Fatalf("aliased sender edit was not applied: %+v", msg)
	}
}

func TestLiveSyncRejectsSecretMessageEditWithUnhandledPayload(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			EditedMessage: &waE2E.Message{StickerSyncRmrMessage: &waE2E.StickerSyncRMRMessage{
				Filehash: []string{"abc"},
			}},
		}}, nil
	}
	webhooks := 0
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat},
				ID:            "edit-event",
				Timestamp:     base.Add(time.Minute),
			},
			Message: secretEditEnvelope(chat, "original-id"),
		}, &messagesStored, func(string, string) {}, func(wa.ParsedMessage) { webhooks++ })
	})

	if !strings.Contains(out, "contains unsupported payload stickerSyncRmrMessage") {
		t.Fatalf("expected unsupported payload warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited {
		t.Fatalf("unsupported edit changed original: %+v", msg)
	}
	if webhooks != 0 {
		t.Fatalf("unsupported edit published %d webhooks", webhooks)
	}
}

func TestLiveSyncAcceptsIncomingSecretMessageEditWithSenderRelativeFromMe(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: false},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("edited body"), nil
	}
	envelope := secretEditEnvelope(chat, "original-id")
	envelope.SecretEncryptedMessage.TargetMessageKey.FromMe = proto.Bool(true)
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: false},
			ID:            "edit-event",
			Timestamp:     base.Add(time.Minute),
		},
		Message: envelope,
	}, &messagesStored, func(string, string) {}, nil)

	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage edited original: %v", err)
	}
	if msg.Text != "edited body" || !msg.Edited || msg.FromMe {
		t.Fatalf("sender-relative edit was not normalized: %+v", msg)
	}
}

func TestLiveSyncRejectsSecretMessageEditWithRedirectedChat(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	group := types.JID{User: "120363000000", Server: types.GroupServer}
	otherGroup := types.JID{User: "120363000001", Server: types.GroupServer}
	sender := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: group, Sender: sender, IsGroup: true},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("redirected body"), nil
	}
	webhooks := 0
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: group, Sender: sender, IsGroup: true},
				ID:            "edit-event",
				Timestamp:     base.Add(time.Minute),
			},
			Message: secretGroupEditEnvelope(otherGroup, sender, "original-id"),
		}, &messagesStored, func(string, string) {}, func(wa.ParsedMessage) { webhooks++ })
	})

	if !strings.Contains(out, "target chat does not match the authenticated chat") {
		t.Fatalf("expected chat mismatch warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(group.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited {
		t.Fatalf("redirected edit changed original: %+v", msg)
	}
	if webhooks != 0 {
		t.Fatalf("redirected edit published %d webhooks", webhooks)
	}
}

func TestLiveSyncRejectsSecretMessageEditWithNestedMutation(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			EditedMessage: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key:           &waCommon.MessageKey{ID: proto.String("other-id"), RemoteJID: proto.String(chat.String())},
				EditedMessage: &waE2E.Message{Conversation: proto.String("redirected body")},
			}},
		}}, nil
	}
	webhooks := 0
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat},
				ID:            "edit-event",
				Timestamp:     base.Add(time.Minute),
			},
			Message: secretEditEnvelope(chat, "original-id"),
		}, &messagesStored, func(string, string) {}, func(wa.ParsedMessage) { webhooks++ })
	})

	if !strings.Contains(out, "contains a nested protocol mutation") {
		t.Fatalf("expected nested mutation warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited {
		t.Fatalf("nested edit changed original: %+v", msg)
	}
	if webhooks != 0 {
		t.Fatalf("nested edit published %d webhooks", webhooks)
	}
}

func TestLiveSyncRejectsSecretMessageEditWithWrappedRevoke(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            "original-id",
			Timestamp:     base,
		},
		Message: &waProto.Message{Conversation: proto.String("original body")},
	}, &messagesStored, func(string, string) {}, nil)

	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return wrappedRevokeProtocolEdit(chat, "original-id"), nil
	}
	webhooks := 0
	out := captureStderr(t, func() {
		a.handleLiveSyncMessage(context.Background(), SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat},
				ID:            "edit-event",
				Timestamp:     base.Add(time.Minute),
			},
			Message: secretEditEnvelope(chat, "original-id"),
		}, &messagesStored, func(string, string) {}, func(wa.ParsedMessage) { webhooks++ })
	})

	if !strings.Contains(out, "contains a nested protocol mutation") {
		t.Fatalf("expected nested mutation warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited || msg.Revoked {
		t.Fatalf("wrapped revoke changed original: %+v", msg)
	}
	if webhooks != 0 {
		t.Fatalf("wrapped revoke published %d webhooks", webhooks)
	}
}

func TestHistorySyncDecryptsSecretMessageEditBeforeStorage(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("edited body"), nil
	}
	editMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("edit-event"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(time.Minute).Unix())),
		Message:          secretEditEnvelope(chat, "original-id"),
	}
	originalMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("original-id"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("original body")},
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       proto.String(chat.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: editMsg}, {Message: originalMsg}},
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage edited original: %v", err)
	}
	if msg.Text != "edited body" || !msg.Edited {
		t.Fatalf("encrypted history edit was not applied: %+v", msg)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 1 {
		t.Fatalf("expected only the original row, got %d (err=%v)", n, err)
	}
}

func TestHistorySyncRejectsSecretMessageEditFromDifferentGroupParticipant(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	group := types.JID{User: "120363000000", Server: types.GroupServer}
	originalSender := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	editor := types.JID{User: "15550000002", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("unauthorized body"), nil
	}
	editMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID:   proto.String(group.String()),
			Participant: proto.String(editor.String()),
			FromMe:      proto.Bool(false),
			ID:          proto.String("edit-event"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(time.Minute).Unix())),
		Message:          secretGroupEditEnvelope(group, originalSender, "original-id"),
	}
	originalMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID:   proto.String(group.String()),
			Participant: proto.String(originalSender.String()),
			FromMe:      proto.Bool(false),
			ID:          proto.String("original-id"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("original body")},
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       proto.String(group.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: editMsg}, {Message: originalMsg}},
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	out := captureStderr(t, func() {
		a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})
	})

	if !strings.Contains(out, "sender does not own the target message") {
		t.Fatalf("expected sender mismatch warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(group.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited {
		t.Fatalf("unauthorized history edit changed original: %+v", msg)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 1 {
		t.Fatalf("expected only the original row, got %d (err=%v)", n, err)
	}
}

func TestHistorySyncAcceptsIncomingSecretMessageEditWithSenderRelativeFromMe(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return decryptedProtocolEdit("edited body"), nil
	}
	envelope := secretEditEnvelope(chat, "original-id")
	envelope.SecretEncryptedMessage.TargetMessageKey.FromMe = proto.Bool(true)
	editMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("edit-event"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(time.Minute).Unix())),
		Message:          envelope,
	}
	originalMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("original-id"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("original body")},
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       proto.String(chat.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: editMsg}, {Message: originalMsg}},
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})

	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage edited original: %v", err)
	}
	if msg.Text != "edited body" || !msg.Edited || msg.FromMe {
		t.Fatalf("sender-relative history edit was not normalized: %+v", msg)
	}
}

func TestHistorySyncRejectsSecretMessageEditWithWrappedRevoke(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	f.decryptSecretFunc = func(_ *events.Message) (*waE2E.Message, error) {
		return wrappedRevokeProtocolEdit(chat, "original-id"), nil
	}
	editMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("edit-event"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(time.Minute).Unix())),
		Message:          secretEditEnvelope(chat, "original-id"),
	}
	originalMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("original-id"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("original body")},
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID:       proto.String(chat.String()),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: editMsg}, {Message: originalMsg}},
		}},
	}}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	out := captureStderr(t, func() {
		a.handleHistorySync(context.Background(), SyncOptions{}, history, &messagesStored, &lastEvent, func(string, string) {})
	})

	if !strings.Contains(out, "contains a nested protocol mutation") {
		t.Fatalf("expected nested mutation warning, got:\n%s", out)
	}
	msg, err := a.db.GetMessage(chat.String(), "original-id")
	if err != nil {
		t.Fatalf("GetMessage original: %v", err)
	}
	if msg.Text != "original body" || msg.Edited || msg.Revoked {
		t.Fatalf("wrapped history revoke changed original: %+v", msg)
	}
	if n, err := a.db.CountMessages(); err != nil || n != 1 {
		t.Fatalf("expected only the original row, got %d (err=%v)", n, err)
	}
}

func secretEditEnvelope(chat types.JID, targetID string) *waProto.Message {
	return &waProto.Message{
		SecretEncryptedMessage: &waE2E.SecretEncryptedMessage{
			TargetMessageKey: &waCommon.MessageKey{
				ID:        proto.String(targetID),
				RemoteJID: proto.String(chat.String()),
			},
			SecretEncType: waE2E.SecretEncryptedMessage_MESSAGE_EDIT.Enum(),
		},
	}
}

func secretGroupEditEnvelope(chat, sender types.JID, targetID string) *waProto.Message {
	msg := secretEditEnvelope(chat, targetID)
	msg.SecretEncryptedMessage.TargetMessageKey.Participant = proto.String(sender.String())
	return msg
}

func decryptedProtocolEdit(body string) *waE2E.Message {
	return &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			EditedMessage: &waE2E.Message{Conversation: proto.String(body)},
		},
	}
}

func wrappedRevokeProtocolEdit(chat types.JID, targetID string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		EditedMessage: &waE2E.Message{EditedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
				Key: &waCommon.MessageKey{
					ID:        proto.String(targetID),
					RemoteJID: proto.String(chat.String()),
				},
			}},
		}},
	}}
}
