package app

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// unreadChat is a chat with one incoming message that arrived after readAt and
// is counted as unread, the state a read signal from before it must not erase.
func unreadChat(t *testing.T, a *App, readAt time.Time) types.JID {
	t.Helper()
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", readAt.Add(time.Hour)); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   chat.String(),
		MsgID:     "after-the-read",
		SenderJID: chat.String(),
		Timestamp: readAt.Add(time.Hour),
		Text:      "are you there?",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	if err := a.db.SetChatUnreadCount(chat.String(), 1); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}
	return chat
}

func TestMarkChatAsReadKeepsWhatArrivedAfterTheRead(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	readAt := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	chat := unreadChat(t, a, readAt)

	// A full sync replays the read as it was made, hours after the fact.
	a.handleChatStateEvent(context.Background(), &events.MarkChatAsRead{
		JID:       chat,
		Timestamp: readAt.Add(24 * time.Hour),
		Action: &waSyncAction.MarkChatAsReadAction{
			Read: proto.Bool(true),
			MessageRange: &waSyncAction.SyncActionMessageRange{
				LastMessageTimestamp: proto.Int64(readAt.Unix()),
			},
		},
		FromFullSync: true,
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("chat after a replayed read = %+v, want the later message still unread", c)
	}
}

func TestMarkChatAsReadClearsWhatItCovered(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	readAt := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	chat := unreadChat(t, a, readAt)

	a.handleChatStateEvent(context.Background(), &events.MarkChatAsRead{
		JID:       chat,
		Timestamp: readAt.Add(2 * time.Hour),
		Action: &waSyncAction.MarkChatAsReadAction{
			Read: proto.Bool(true),
			MessageRange: &waSyncAction.SyncActionMessageRange{
				LastMessageTimestamp: proto.Int64(readAt.Add(2 * time.Hour).Unix()),
			},
		},
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Unread || c.UnreadCount != 0 {
		t.Fatalf("chat after a read that covered everything = %+v, want it read", c)
	}
}

func TestMarkChatAsReadWithoutRangeUsesItsOwnTime(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	readAt := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	chat := unreadChat(t, a, readAt)

	a.handleChatStateEvent(context.Background(), &events.MarkChatAsRead{
		JID:       chat,
		Timestamp: readAt,
		Action:    &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true)},
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("chat after a read with no range = %+v, want the later message still unread", c)
	}
}

func TestMarkChatAsUnreadStillSetsTheMarker(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", time.Now()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	a.handleChatStateEvent(context.Background(), &events.MarkChatAsRead{
		JID:    chat,
		Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)},
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 0 {
		t.Fatalf("chat marked unread elsewhere = %+v", c)
	}
}

func TestReadSelfReceiptKeepsWhatArrivedAfterIt(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	readAt := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	chat := unreadChat(t, a, readAt)

	a.handleReceiptPersistenceEvent(context.Background(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		Type:          types.ReceiptTypeReadSelf,
		Timestamp:     readAt,
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("chat after an older read-self receipt = %+v, want the later message still unread", c)
	}
}

func TestReadSelfReceiptClearsWhatItCovered(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	readAt := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	chat := unreadChat(t, a, readAt)

	a.handleReceiptPersistenceEvent(context.Background(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		Type:          types.ReceiptTypeReadSelf,
		Timestamp:     readAt.Add(2 * time.Hour),
	})

	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if c.Unread || c.UnreadCount != 0 {
		t.Fatalf("chat after a current read-self receipt = %+v, want it read", c)
	}
}
