package app

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestReplayedReadDoesNotRestoreAlreadyReadMessages(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	at := time.Unix(1700000000, 0)
	chat := unreadChat(t, a, at)
	for _, through := range []time.Time{at.Add(2 * time.Hour), at} {
		if err := a.handleChatStateEvent(context.Background(), &events.MarkChatAsRead{JID: chat, Timestamp: through, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true)}}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.Unread || c.UnreadCount != 0 {
		t.Fatalf("old read restored already-read messages: %+v", c)
	}
}

func TestReadBoundaryPreservesMessageAtSameSecond(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	at := time.Unix(1700000000, 0)
	chat := unreadChat(t, a, at)
	at = at.Add(time.Hour)
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "later-in-the-same-second", Timestamp: at, Text: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatUnreadCount(chat.String(), 2); err != nil {
		t.Fatal(err)
	}
	event := &events.MarkChatAsRead{JID: chat, Timestamp: at.Add(time.Hour), Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true), MessageRange: &waSyncAction.SyncActionMessageRange{LastMessageTimestamp: proto.Int64(at.Unix()), Messages: []*waSyncAction.SyncActionMessage{{Key: &waCommon.MessageKey{ID: proto.String("after-the-read")}, Timestamp: proto.Int64(at.Unix())}}}}}
	if err := a.handleChatStateEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("same-second arrival was cleared: %+v", c)
	}
}

func TestReadSelfUsesMessageBoundaryInsteadOfReceiptTime(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	at := time.Unix(1700000000, 0)
	chat := unreadChat(t, a, at)
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "covered", Timestamp: at, Text: "old"}); err != nil {
		t.Fatal(err)
	}
	a.handleReceiptPersistenceEvent(context.Background(), &events.Receipt{MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeReadSelf, Timestamp: at.Add(2 * time.Hour), MessageIDs: []types.MessageID{"covered"}})
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("receipt time cleared a newer message: %+v", c)
	}
}

func TestLiveUnreadIgnoresNonReadableEvents(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	for _, pm := range []wa.ParsedMessage{
		{Chat: chat, ID: "system"},
		{Chat: chat, ID: "reaction", ReactionToID: "old", ReactionEmoji: "ok"},
		{Chat: chat, ID: "revoke", Revoked: true},
	} {
		if a.shouldIncrementLiveUnread(context.Background(), pm) {
			t.Errorf("%s would increase unread", pm.ID)
		}
	}
	if !a.shouldIncrementLiveUnread(context.Background(), wa.ParsedMessage{Chat: chat, ID: "text", Text: "hello"}) {
		t.Fatal("incoming content must increase unread")
	}
}
