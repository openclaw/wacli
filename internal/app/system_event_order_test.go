package app

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// A WhatsApp system event, such as a changed security code or a group notice,
// reaches sync as a payload the parser finds no content in and is stored with
// the "(message)" placeholder.
func systemEvent(chat types.JID, id string, ts time.Time) wa.ParsedMessage {
	return wa.ParsedMessage{Chat: chat, ID: id, SenderJID: chat.String(), Timestamp: ts}
}

func TestSystemEventDoesNotBecomeTheChatsNewestMessage(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	spoke := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)

	if err := a.storeParsedMessage(context.Background(), wa.ParsedMessage{
		Chat: chat, ID: "m-real", SenderJID: chat.String(), Timestamp: spoke, Text: "hello",
	}); err != nil {
		t.Fatalf("storeParsedMessage: %v", err)
	}
	if err := a.storeParsedMessage(context.Background(), systemEvent(chat, "m-system", spoke.Add(48*time.Hour))); err != nil {
		t.Fatalf("storeParsedMessage system event: %v", err)
	}

	stored, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !stored.LastMessageTS.Equal(spoke) {
		t.Fatalf("chat list time = %s, want the last real message at %s", stored.LastMessageTS, spoke)
	}
	// The row itself is still kept, so the event is not lost.
	if _, err := a.db.GetMessage(chat.String(), "m-system"); err != nil {
		t.Fatalf("GetMessage system event: %v", err)
	}
}

func TestSystemEventInANewChatLeavesItWithoutAListTime(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "456", Server: types.DefaultUserServer}

	if err := a.storeParsedMessage(context.Background(),
		systemEvent(chat, "m-system", time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC))); err != nil {
		t.Fatalf("storeParsedMessage: %v", err)
	}

	stored, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !stored.LastMessageTS.IsZero() {
		t.Fatalf("chat list time = %s, want none: nothing has been said there", stored.LastMessageTS)
	}
}

func TestMessagesWithContentStillSetTheChatsListTime(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "789", Server: types.DefaultUserServer}
	base := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		message wa.ParsedMessage
		at      time.Time
	}{
		{"text", wa.ParsedMessage{ID: "m-text", Text: "hello"}, base},
		{"media", wa.ParsedMessage{ID: "m-media", Media: &wa.Media{Type: "image"}}, base.Add(time.Hour)},
		{"reaction", wa.ParsedMessage{ID: "m-reaction", ReactionToID: "m-text", ReactionEmoji: "👍"}, base.Add(2 * time.Hour)},
		{"revoked", wa.ParsedMessage{ID: "m-revoked", Revoked: true}, base.Add(3 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pm := tc.message
			pm.Chat = chat
			pm.SenderJID = chat.String()
			pm.Timestamp = tc.at
			if err := a.storeParsedMessage(context.Background(), pm); err != nil {
				t.Fatalf("storeParsedMessage: %v", err)
			}
			stored, err := a.db.GetChat(chat.String())
			if err != nil {
				t.Fatalf("GetChat: %v", err)
			}
			if !stored.LastMessageTS.Equal(tc.at) {
				t.Fatalf("chat list time = %s, want %s", stored.LastMessageTS, tc.at)
			}
		})
	}
}
