package app

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func historySyncOf(chat types.JID, msgs ...*waWeb.WebMessageInfo) *events.HistorySync {
	conv := &waHistorySync.Conversation{ID: proto.String(chat.String())}
	for _, msg := range msgs {
		conv.Messages = append(conv.Messages, &waHistorySync.HistorySyncMsg{Message: msg})
	}
	return &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType:      waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{conv},
	}}
}

func TestHistorySyncStoresContentInsideEphemeralWrapper(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "393331234567", Server: types.DefaultUserServer}
	msg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("ephemeral-1"),
		},
		MessageTimestamp: proto.Uint64(uint64(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC).Unix())),
		Message: &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{
					ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("Siamo interessati, chiamateci")},
				},
			},
		},
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, historySyncOf(chat, msg), &messagesStored, &lastEvent, func(string, string) {})

	stored, err := a.db.GetMessage(chat.String(), "ephemeral-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if stored.Text != "Siamo interessati, chiamateci" {
		t.Fatalf("Text = %q, want wrapped text", stored.Text)
	}
}

// Once wrappers are unwrapped the reaction target is known, so the encrypted
// reaction must be decrypted too: an empty emoji would read as a removal.
func TestHistorySyncDecryptsEncryptedReactionInsideEphemeralWrapper(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	f.contacts[chat.ToNonAD()] = types.ContactInfo{Found: true, FullName: "Alice"}
	f.decryptedReaction = &waProto.ReactionMessage{
		Text: proto.String("❤️"),
		Key:  &waCommon.MessageKey{ID: proto.String("m-text")},
	}

	base := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	textMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("m-text"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Unix())),
		Message:          &waProto.Message{Conversation: proto.String("hello")},
	}
	reactionMsg := &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(false),
			ID:        proto.String("m-enc-react"),
		},
		MessageTimestamp: proto.Uint64(uint64(base.Add(time.Second).Unix())),
		Message: &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{
				Message: &waProto.Message{
					EncReactionMessage: &waProto.EncReactionMessage{
						TargetMessageKey: &waCommon.MessageKey{ID: proto.String("m-text")},
					},
				},
			},
		},
	}

	var messagesStored atomic.Int64
	var lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, historySyncOf(chat, textMsg, reactionMsg), &messagesStored, &lastEvent, func(string, string) {})

	msg, err := a.db.GetMessage(chat.String(), "m-enc-react")
	if err != nil {
		t.Fatalf("GetMessage encrypted reaction: %v", err)
	}
	if msg.ReactionToID != "m-text" || msg.ReactionEmoji != "❤️" {
		t.Fatalf("unexpected reaction fields: to=%q emoji=%q", msg.ReactionToID, msg.ReactionEmoji)
	}
}

func TestHistoryPayloadsThroughCLI(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	f.decryptedReaction = &waProto.ReactionMessage{
		Text: proto.String("❤️"), Key: &waCommon.MessageKey{ID: proto.String("text")},
	}
	wrap := func(m *waProto.Message) *waProto.Message {
		return &waProto.Message{EphemeralMessage: &waProto.FutureProofMessage{Message: m}}
	}
	payloads := []*waProto.Message{
		wrap(&waProto.Message{Conversation: proto.String("synthetic wrapped needle")}),
		{ContactMessage: &waProto.ContactMessage{Vcard: proto.String("BEGIN:VCARD\nitem1.FN:Synthetic Contact\nitem1.TEL:+1 555 000 0002\nEND:VCARD")}},
		wrap(&waProto.Message{PtvMessage: &waProto.VideoMessage{
			Mimetype: proto.String("video/mp4"), DirectPath: proto.String("/synthetic-video"),
			ContextInfo: &waProto.ContextInfo{StanzaID: proto.String("text"), Participant: proto.String(chat.String()), QuotedMessage: &waProto.Message{Conversation: proto.String("synthetic wrapped needle")}},
		}}),
		wrap(&waProto.Message{EncReactionMessage: &waProto.EncReactionMessage{TargetMessageKey: &waCommon.MessageKey{ID: proto.String("text")}}}),
	}
	ids := []string{"text", "contact", "video", "reaction"}
	var messages []*waWeb.WebMessageInfo
	for i, payload := range payloads {
		messages = append(messages, &waWeb.WebMessageInfo{
			Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), ID: proto.String(ids[i])},
			MessageTimestamp: proto.Uint64(1700000000 + uint64(i)), Message: payload,
		})
	}
	var stored, lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, historySyncOf(chat, messages...), &stored, &lastEvent, func(string, string) {})
	h := newRetrievalCLI(t, a)
	wants := []map[string]string{
		{"Text": "synthetic wrapped needle"},
		{"Text": "Contact: Synthetic Contact (+1 555 000 0002)"},
		{"MediaType": "video", "quoted_msg_id": "text", "DisplayText": "> synthetic wrapped needle\nSent video"},
		{"ReactionToID": "text", "ReactionEmoji": "❤️"},
	}
	for i, id := range ids {
		got := h.run(a.StoreDir(), "", "messages", "show", "--chat", chat.String(), "--id", id)
		for key, want := range wants[i] {
			if got[key] != want {
				t.Errorf("%s %s = %v, want %q", id, key, got[key], want)
			}
		}
	}
	got := retrievalIDs(h.run(a.StoreDir(), "", "messages", "search", "needle", "--chat", chat.String()))
	slices.Sort(got)
	if !slices.Equal(got, []string{"reaction", "text", "video"}) {
		t.Fatalf("search IDs = %v, want reaction, text, video", got)
	}
}
