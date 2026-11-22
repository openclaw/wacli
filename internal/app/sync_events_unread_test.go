package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveSyncCountsSemanticMessagesOnce(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		message *waE2E.Message
	}{
		{"event", &waE2E.Message{EventMessage: &waE2E.EventMessage{}}},
		{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{}}},
		{"contacts", &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{}}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			ctx := context.Background()
			chat := types.NewJID("123", types.DefaultUserServer)
			info := types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat},
				ID:            tc.kind,
				Timestamp:     time.Date(2024, 1, 3, 10, 1, 0, 0, time.UTC),
			}
			message := &events.Message{Info: info, Message: tc.message}
			var stored atomic.Int64
			assertUnread := func(stage string, want int) {
				t.Helper()
				c, err := a.db.GetChat(chat.String())
				if err != nil || c.UnreadCount != want || c.Unread != (want > 0) {
					t.Errorf("%s: chat = %+v, err %v, want unread count %d", stage, c, err, want)
				}
			}
			a.handleLiveSyncMessage(ctx, SyncOptions{}, message, &stored, nil, nil)
			assertUnread("first delivery", 1)
			m, err := a.db.GetMessage(chat.String(), tc.kind)
			if err != nil || m.Kind != tc.kind || m.Text != "" || m.DisplayText != "(message)" {
				t.Fatalf("semantic-only fixture = %+v, err %v", m, err)
			}
			a.handleLiveSyncMessage(ctx, SyncOptions{}, message, &stored, nil, nil)
			assertUnread("duplicate", 1)
			if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
				t.Fatal(err)
			}
			// A protocol replay must not downgrade semantic content before the
			// next decoded replay, including after the chat has been read.
			captureStderr(t, func() {
				a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{Info: info, Message: &waE2E.Message{}}, &stored, nil, nil)
			})
			a.handleLiveSyncMessage(ctx, SyncOptions{}, message, &stored, nil, nil)
			assertUnread("replay after read", 0)
		})
	}
}

func TestSemanticUnreadSurvivesBoundedRead(t *testing.T) {
	for _, source := range []string{"live", "history"} {
		t.Run(source, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			ctx := context.Background()
			chat := types.NewJID("123", types.DefaultUserServer)
			at := time.Date(2024, 1, 3, 10, 0, 0, 0, time.UTC)
			text := &waE2E.Message{Conversation: proto.String("hello")}
			event := &waE2E.Message{EventMessage: &waE2E.EventMessage{}}
			var stored, last atomic.Int64
			if source == "live" {
				for i, message := range []*waE2E.Message{text, event} {
					a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
						Info: types.MessageInfo{
							MessageSource: types.MessageSource{Chat: chat, Sender: chat},
							ID:            []string{"text", "event"}[i],
							Timestamp:     at.Add(time.Duration(i) * time.Minute),
						},
						Message: message,
					}, &stored, nil, nil)
				}
			} else {
				history := historySyncOf(chat,
					&waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("text"), RemoteJID: proto.String(chat.String())}, MessageTimestamp: proto.Uint64(uint64(at.Unix())), Message: text},
					&waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("event"), RemoteJID: proto.String(chat.String())}, MessageTimestamp: proto.Uint64(uint64(at.Add(time.Minute).Unix())), Message: &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: event}}},
				)
				// History owns the phone's count, rather than incrementing per row.
				history.Data.Conversations[0].UnreadCount = proto.Uint32(2)
				a.handleHistorySync(ctx, SyncOptions{}, history, &stored, &last, nil)
			}
			c, err := a.db.GetChat(chat.String())
			if err != nil || !c.Unread || c.UnreadCount != 2 {
				t.Fatalf("after ingestion: chat = %+v, err %v", c, err)
			}
			if err := a.db.ClearChatUnreadThrough(chat.String(), at, []string{"text"}); err != nil {
				t.Fatal(err)
			}
			c, err = a.db.GetChat(chat.String())
			if err != nil || !c.Unread || c.UnreadCount != 1 {
				t.Fatalf("read through 10:00 cleared the 10:01 event: chat = %+v, err %v", c, err)
			}
			sent, _, err := a.MarkChatReadWithReceiptsThrough(ctx, chat, "event")
			if err != nil || sent != 1 {
				t.Fatalf("read through event: sent = %d, err %v", sent, err)
			}
			c, err = a.db.GetChat(chat.String())
			if err != nil || c.Unread || c.UnreadCount != 0 {
				t.Fatalf("after reading event: chat = %+v, err %v", c, err)
			}
		})
	}
}

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
