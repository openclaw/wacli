package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveSyncCountsSenderKeyPlaceholderUpgradeOnce(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	ctx := context.Background()
	chat := types.JID{User: "456", Server: types.GroupServer}
	sender := types.JID{User: "123", Server: types.DefaultUserServer}
	var stored atomic.Int64
	assertUnread := func(want int) {
		t.Helper()
		c, err := a.db.GetChat(chat.String())
		if err != nil {
			t.Fatalf("GetChat: %v", err)
		}
		if c.UnreadCount != want || c.Unread != (want > 0) {
			t.Fatalf("unread state = %+v, want count %d", c, want)
		}
	}
	assertText := func(id string) {
		t.Helper()
		m, err := a.db.GetMessage(chat.String(), id)
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if m.Text != "hello" || m.DisplayText != "hello" {
			t.Fatalf("stored message after replay = %+v, want retained text", m)
		}
	}

	// A sender-key event and its decoded text can arrive with the same ID.
	// Exercise both observed messages through the live handler, using only
	// synthetic IDs and content, and verify each transition before replaying.
	for i, id := range []string{"group-message-1", "group-message-2"} {
		info := types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: true},
			ID:            id,
			Timestamp:     time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		}
		key := &events.Message{Info: info, Message: &waE2E.Message{
			SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{GroupID: proto.String(chat.String())},
		}}
		if pm := wa.ParseLiveMessage(key); pm.HasContent() || pm.UnhandledPayload != "senderKeyDistributionMessage" {
			t.Fatalf("sender-key fixture parsed as %+v, want contentless protocol payload", pm)
		}
		captureStderr(t, func() {
			a.handleLiveSyncMessage(ctx, SyncOptions{}, key, &stored, nil, nil)
			a.handleLiveSyncMessage(ctx, SyncOptions{}, key, &stored, nil, nil)
		})
		assertUnread(i)
		m, err := a.db.GetMessage(chat.String(), id)
		if err != nil || m.Text != "" || m.DisplayText != "(message)" {
			t.Fatalf("sender-key placeholder = %+v, err %v", m, err)
		}

		text := &events.Message{Info: info, Message: &waE2E.Message{Conversation: proto.String("hello")}}
		a.handleLiveSyncMessage(ctx, SyncOptions{}, text, &stored, nil, nil)
		assertUnread(i + 1)
		assertText(id)
		a.handleLiveSyncMessage(ctx, SyncOptions{}, text, &stored, nil, nil)
		assertUnread(i + 1)

		// A late protocol replay must not downgrade the row: otherwise a
		// subsequent text replay would look like another first transition.
		captureStderr(t, func() {
			a.handleLiveSyncMessage(ctx, SyncOptions{}, key, &stored, nil, nil)
		})
		assertText(id)
		a.handleLiveSyncMessage(ctx, SyncOptions{}, text, &stored, nil, nil)
		assertUnread(i + 1)
	}

	// Once the chat is read, duplicates must not resurrect its unread count.
	if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
		t.Fatalf("clear unread: %v", err)
	}
	a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsGroup: true},
			ID:            "group-message-2",
			Timestamp:     time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}, &stored, nil, nil)
	assertUnread(0)
}

func TestLiveSyncPlaceholderUpgradeDoesNotCountOwnMessagesOrReactions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fromMe  bool
		message *waE2E.Message
	}{
		{name: "own", fromMe: true, message: &waE2E.Message{Conversation: proto.String("sent")}},
		{name: "reaction", message: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key: &waCommon.MessageKey{ID: proto.String("target")}, Text: proto.String("👍"),
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			ctx := context.Background()
			chat := types.JID{User: "456", Server: types.GroupServer}
			info := types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: types.JID{User: "123", Server: types.DefaultUserServer}, IsGroup: true, IsFromMe: tc.fromMe},
				ID:            "excluded-upgrade",
				Timestamp:     time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
			}
			var stored atomic.Int64
			key := &events.Message{Info: info, Message: &waE2E.Message{SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{}}}
			captureStderr(t, func() {
				a.handleLiveSyncMessage(ctx, SyncOptions{}, key, &stored, nil, nil)
			})
			message := &events.Message{Info: info, Message: tc.message}
			a.handleLiveSyncMessage(ctx, SyncOptions{}, message, &stored, nil, nil)
			// Include the same late protocol/text replay that broke inbound
			// unread counts, without changing the existing exclusion rules.
			a.handleLiveSyncMessage(ctx, SyncOptions{}, key, &stored, nil, nil)
			a.handleLiveSyncMessage(ctx, SyncOptions{}, message, &stored, nil, nil)
			c, err := a.db.GetChat(chat.String())
			if err != nil || c.Unread || c.UnreadCount != 0 {
				t.Fatalf("excluded message unread state = %+v, err %v", c, err)
			}
			m, err := a.db.GetMessage(chat.String(), info.ID)
			if err != nil || m.DisplayText == "(message)" {
				t.Fatalf("excluded message content = %+v, err %v", m, err)
			}
		})
	}
}
