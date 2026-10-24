package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var ownReadBase = time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC)

func liveMessage(chat types.JID, id string, fromMe bool, at time.Time, msg *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer},
			ID:            id,
			Timestamp:     at,
			PushName:      "Alice",
		},
		Message: msg,
	}
}

func textMessage(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func feedLive(t *testing.T, a *App, evt *events.Message) {
	t.Helper()
	var stored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &stored, func(string, string) {}, nil)
}

func requireUnread(t *testing.T, a *App, chat types.JID, unread bool, count int) {
	t.Helper()
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Unread != unread || c.UnreadCount != count {
		t.Fatalf("unread state = (%v, %d), want (%v, %d)", c.Unread, c.UnreadCount, unread, count)
	}
}

func TestLiveOwnReplyMarksChatReadDirectAndGroup(t *testing.T) {
	for _, chat := range []types.JID{
		{User: "5551230000", Server: types.DefaultUserServer},
		{User: "120363000000000001", Server: types.GroupServer},
	} {
		t.Run(chat.Server, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			feedLive(t, a, liveMessage(chat, "in-1", false, ownReadBase, textMessage("hello")))
			feedLive(t, a, liveMessage(chat, "in-2", false, ownReadBase.Add(time.Minute), textMessage("anyone?")))
			requireUnread(t, a, chat, true, 2)

			// Sent from the phone or another linked device: arrives as a FromMe event.
			own := liveMessage(chat, "own-1", true, ownReadBase.Add(2*time.Minute), textMessage("here"))
			feedLive(t, a, own)
			requireUnread(t, a, chat, false, 0)

			// Replaying the same event is idempotent, and a new incoming message
			// after the reply is unread again.
			feedLive(t, a, own)
			requireUnread(t, a, chat, false, 0)
			feedLive(t, a, liveMessage(chat, "in-3", false, ownReadBase.Add(3*time.Minute), textMessage("thanks")))
			requireUnread(t, a, chat, true, 1)
			feedLive(t, a, own)
			requireUnread(t, a, chat, true, 1)
		})
	}
}

func TestLiveOwnReplyClearsManualUnreadMarker(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "5551230000", Server: types.DefaultUserServer}
	feedLive(t, a, liveMessage(chat, "in-1", false, ownReadBase, textMessage("hello")))
	if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatUnread(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	feedLive(t, a, liveMessage(chat, "own-1", true, ownReadBase.Add(time.Minute), textMessage("ok")))
	requireUnread(t, a, chat, false, 0)
}

func TestLiveOwnReplyOlderThanIncomingKeepsUnread(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "5551230000", Server: types.DefaultUserServer}
	feedLive(t, a, liveMessage(chat, "in-1", false, ownReadBase.Add(5*time.Minute), textMessage("later")))
	feedLive(t, a, liveMessage(chat, "own-old", true, ownReadBase, textMessage("older reply delivered late")))
	requireUnread(t, a, chat, true, 1)
}

func TestLiveOwnNonReplyEventsKeepChatUnread(t *testing.T) {
	chat := types.JID{User: "5551230000", Server: types.DefaultUserServer}
	for _, tc := range []struct {
		name string
		msg  *waE2E.Message
	}{
		{"reaction", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key:  &waCommon.MessageKey{ID: proto.String("in-1"), RemoteJID: proto.String(chat.String())},
			Text: proto.String("👍"),
		}}},
		{"edit", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:           &waCommon.MessageKey{ID: proto.String("own-orig"), FromMe: proto.Bool(true)},
			EditedMessage: textMessage("fixed"),
		}}},
		{"revoke", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_REVOKE.Enum(),
			Key:  &waCommon.MessageKey{ID: proto.String("own-orig"), FromMe: proto.Bool(true)},
		}}},
		{"protocol", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type:                waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(),
			EphemeralExpiration: proto.Uint32(86400),
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			feedLive(t, a, liveMessage(chat, "in-1", false, ownReadBase, textMessage("hello")))
			feedLive(t, a, liveMessage(chat, "own-x", true, ownReadBase.Add(time.Minute), tc.msg))
			requireUnread(t, a, chat, true, 1)
		})
	}
}

func historyMessage(chat types.JID, id string, fromMe bool, at time.Time, text string) *waHistorySync.HistorySyncMsg {
	return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(fromMe),
			ID:        proto.String(id),
		},
		MessageTimestamp: proto.Uint64(uint64(at.Unix())),
		Message:          textMessage(text),
	}}
}

func runHistory(t *testing.T, a *App, conv *waHistorySync.Conversation) {
	t.Helper()
	var stored, lastEvent atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType:      waHistorySync.HistorySync_FULL.Enum(),
		Conversations: []*waHistorySync.Conversation{conv},
	}}, &stored, &lastEvent, func(string, string) {})
}

func TestHistoryOldOwnMessageDoesNotClearLaterUnread(t *testing.T) {
	chat := types.JID{User: "5551230000", Server: types.DefaultUserServer}
	// Newest-first and oldest-first batches must agree, and a count reported by
	// the conversation is authoritative.
	for _, order := range [][]*waHistorySync.HistorySyncMsg{
		{
			historyMessage(chat, "own-old", true, ownReadBase, "old reply"),
			historyMessage(chat, "in-1", false, ownReadBase.Add(time.Minute), "later 1"),
			historyMessage(chat, "in-2", false, ownReadBase.Add(2*time.Minute), "later 2"),
		},
		{
			historyMessage(chat, "in-2", false, ownReadBase.Add(2*time.Minute), "later 2"),
			historyMessage(chat, "in-1", false, ownReadBase.Add(time.Minute), "later 1"),
			historyMessage(chat, "own-old", true, ownReadBase, "old reply"),
		},
	} {
		a := newTestApp(t)
		a.wa = newFakeWA()
		runHistory(t, a, &waHistorySync.Conversation{ID: proto.String(chat.String()), UnreadCount: proto.Uint32(2), Messages: order})
		requireUnread(t, a, chat, true, 2)
	}

	// Without a reported count, local unread state from earlier live messages
	// must survive an older own message too, whatever the batch order.
	for _, order := range [][]*waHistorySync.HistorySyncMsg{
		{historyMessage(chat, "own-old", true, ownReadBase, "old reply"), historyMessage(chat, "in-new", false, ownReadBase.Add(time.Minute), "later")},
		{historyMessage(chat, "in-new", false, ownReadBase.Add(time.Minute), "later"), historyMessage(chat, "own-old", true, ownReadBase, "old reply")},
	} {
		a := newTestApp(t)
		a.wa = newFakeWA()
		if err := a.db.UpsertChat(chat.String(), "dm", "Alice", ownReadBase); err != nil {
			t.Fatal(err)
		}
		if err := a.db.SetChatUnreadCount(chat.String(), 1); err != nil {
			t.Fatal(err)
		}
		runHistory(t, a, &waHistorySync.Conversation{ID: proto.String(chat.String()), Messages: order})
		requireUnread(t, a, chat, true, 1)
	}
}

func TestHistoryNewestOwnMessageClearsUnreadWhenConversationIsSilent(t *testing.T) {
	chat := types.JID{User: "5551230000", Server: types.DefaultUserServer}
	group := types.JID{User: "120363000000000002", Server: types.GroupServer}
	for _, jid := range []types.JID{chat, group} {
		a := newTestApp(t)
		a.wa = newFakeWA()
		feedLive(t, a, liveMessage(jid, "in-live", false, ownReadBase, textMessage("hello")))
		requireUnread(t, a, jid, true, 1)
		runHistory(t, a, &waHistorySync.Conversation{ID: proto.String(jid.String()), Messages: []*waHistorySync.HistorySyncMsg{
			historyMessage(jid, "own-new", true, ownReadBase.Add(time.Minute), "reply"),
			historyMessage(jid, "in-old", false, ownReadBase, "hello"),
		}})
		requireUnread(t, a, jid, false, 0)

		// Idempotent when the same batch is processed again.
		runHistory(t, a, &waHistorySync.Conversation{ID: proto.String(jid.String()), Messages: []*waHistorySync.HistorySyncMsg{
			historyMessage(jid, "own-new", true, ownReadBase.Add(time.Minute), "reply"),
		}})
		requireUnread(t, a, jid, false, 0)
	}
}

func TestHistoryReportedUnreadStateWinsOverNewestOwnMessage(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "5551230000", Server: types.DefaultUserServer}
	runHistory(t, a, &waHistorySync.Conversation{
		ID:             proto.String(chat.String()),
		MarkedAsUnread: proto.Bool(true),
		Messages:       []*waHistorySync.HistorySyncMsg{historyMessage(chat, "own-new", true, ownReadBase, "reply")},
	})
	requireUnread(t, a, chat, true, 0)
}

func TestHistoryOwnReactionDoesNotClearUnread(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "5551230000", Server: types.DefaultUserServer}
	feedLive(t, a, liveMessage(chat, "in-live", false, ownReadBase, textMessage("hello")))
	reaction := historyMessage(chat, "own-react", true, ownReadBase.Add(time.Minute), "")
	reaction.Message.Message = &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
		Key:  &waCommon.MessageKey{ID: proto.String("in-live"), RemoteJID: proto.String(chat.String())},
		Text: proto.String("👍"),
	}}
	runHistory(t, a, &waHistorySync.Conversation{ID: proto.String(chat.String()), Messages: []*waHistorySync.HistorySyncMsg{reaction}})
	requireUnread(t, a, chat, true, 1)
}
