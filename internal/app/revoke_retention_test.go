package app

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	waE2E "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func protocolEvent(chat, sender types.JID, fromMe bool, pm *waE2E.ProtocolMessage) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer},
			ID:            "carrier-" + pm.GetKey().GetID(),
			Timestamp:     time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{ProtocolMessage: pm},
	}
}

func TestRevokeKeepsOriginalPayload(t *testing.T) {
	owner := types.NewJID("15550100001", types.DefaultUserServer)
	contact := types.NewJID("15550100002", types.DefaultUserServer)
	group := types.NewJID("120363001234567890", types.GroupServer)
	admin := types.NewJID("905000000001", types.DefaultUserServer)

	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.groups[group] = &types.GroupInfo{JID: group, GroupName: types.GroupName{Name: "G"}}
	ctx := context.Background()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	media := &wa.Media{Type: "image", Caption: "cap", Filename: "a.jpg", MimeType: "image/jpeg", DirectPath: "/v/x", MediaKey: []byte{1}, FileSHA256: []byte{2}, FileEncSHA256: []byte{3}, FileLength: 42}
	originals := []wa.ParsedMessage{
		{Chat: contact, ID: "dm1", SenderJID: contact.String(), Timestamp: t0, Text: "secret dm"},
		{Chat: contact, ID: "dm2", SenderJID: contact.String(), Timestamp: t0, Text: "cap", Media: media},
		{Chat: group, ID: "g1", SenderJID: contact.String(), Timestamp: t0, Text: "group text"},
		{Chat: group, ID: "g2", SenderJID: contact.String(), Timestamp: t0, Text: "admin target"},
	}
	for _, o := range originals {
		if err := a.storeParsedMessage(ctx, o, messageStoreOptions{}); err != nil {
			t.Fatalf("store original %s: %v", o.ID, err)
		}
	}

	revoke := func(id string) *waE2E.ProtocolMessage {
		return &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waE2E.MessageKey{ID: proto.String(id), FromMe: proto.Bool(true), RemoteJID: proto.String(owner.String())}}
	}
	groupRevoke := func(id string) *waE2E.ProtocolMessage {
		return &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waE2E.MessageKey{ID: proto.String(id), FromMe: proto.Bool(false), RemoteJID: proto.String(group.String()), Participant: proto.String(contact.String())}}
	}
	for _, ev := range []*events.Message{
		protocolEvent(contact, contact, false, revoke("dm1")),
		protocolEvent(contact, contact, false, revoke("dm2")),
		protocolEvent(group, contact, false, groupRevoke("g1")),
		protocolEvent(group, admin, false, groupRevoke("g2")),
	} {
		if err := a.storeParsedMessage(ctx, wa.ParseLiveMessage(ev), messageStoreOptions{}); err != nil {
			t.Fatalf("store revoke: %v", err)
		}
	}

	// A late edit must not clear the tombstone or replace the kept text.
	edit := &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waE2E.MessageKey{ID: proto.String("dm1"), FromMe: proto.Bool(true), RemoteJID: proto.String(owner.String())},
		EditedMessage: &waE2E.Message{Conversation: proto.String("edited after delete")},
	}
	if err := a.storeParsedMessage(ctx, wa.ParseLiveMessage(protocolEvent(contact, contact, false, edit)), messageStoreOptions{}); err != nil {
		t.Fatalf("store edit: %v", err)
	}

	check := func(chat types.JID, id, text, media string) {
		t.Helper()
		m, err := a.db.GetMessage(chat.String(), id)
		if err != nil {
			t.Fatalf("GetMessage %s: %v", id, err)
		}
		if !m.Revoked || m.DeletedAt == nil || m.DeletionReason != store.MessageDeletionReasonWhatsAppRevoke {
			t.Fatalf("%s: not tombstoned: revoked=%v deletedAt=%v reason=%q", id, m.Revoked, m.DeletedAt, m.DeletionReason)
		}
		if m.Text != text {
			t.Fatalf("%s: text = %q, want %q", id, m.Text, text)
		}
		if m.DisplayText == store.DeletedMessageDisplayText || m.DisplayText == "" {
			t.Fatalf("%s: display_text blanked/overwritten: %q", id, m.DisplayText)
		}
		if m.FromMe {
			t.Fatalf("%s: from_me set", id)
		}
		if media != "" && (m.MediaType != media || m.Filename != "a.jpg" || m.MimeType != "image/jpeg" || m.DirectPath != "/v/x") {
			t.Fatalf("%s: media lost: %+v", id, m)
		}
	}
	check(contact, "dm1", "secret dm", "")
	check(contact, "dm2", "cap", "image")
	check(group, "g1", "group text", "")
	check(group, "g2", "admin target", "")

	if _, err := a.db.GetMessage(owner.String(), "dm1"); err == nil {
		t.Fatalf("revoke was filed in the owner's self-chat")
	}
	if m, _ := a.db.GetMessage(contact.String(), "dm1"); m.Edited {
		t.Fatalf("edit after delete re-marked the message as edited")
	}
}
