package app

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestReadByThisAccountElsewhereAcceptsOwnPlainReadReceipts(t *testing.T) {
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	cases := []struct {
		name    string
		receipt events.Receipt
		want    bool
	}{
		{
			// Read receipts turned off: WhatsApp marks the read for our devices only.
			name:    "read-self",
			receipt: events.Receipt{MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeReadSelf},
			want:    true,
		},
		{
			// Read receipts turned on: the phone's read reaches us as an ordinary
			// read receipt sent by this account. This is the common case.
			name:    "our own read",
			receipt: events.Receipt{MessageSource: types.MessageSource{Chat: chat, IsFromMe: true}, Type: types.ReceiptTypeRead},
			want:    true,
		},
		{
			// Someone else read a message we sent: nothing was read here.
			name:    "somebody else's read",
			receipt: events.Receipt{MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeRead},
			want:    false,
		},
		{
			name:    "delivery",
			receipt: events.Receipt{MessageSource: types.MessageSource{Chat: chat, IsFromMe: true}, Type: types.ReceiptTypeDelivered},
			want:    false,
		},
		{
			name:    "played",
			receipt: events.Receipt{MessageSource: types.MessageSource{Chat: chat, IsFromMe: true}, Type: types.ReceiptTypePlayed},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readByThisAccountElsewhere(&tc.receipt); got != tc.want {
				t.Fatalf("readByThisAccountElsewhere = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestOwnPlainReadReceiptClearsTheUnreadCount(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.SetChatUnreadCount(chat.String(), 3); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}

	a.handleReceiptPersistenceEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: true},
		MessageIDs:    []types.MessageID{"incoming-1", "incoming-2"},
		Type:          types.ReceiptTypeRead,
	})

	stored, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.Unread || stored.UnreadCount != 0 {
		t.Fatalf("chat after our own read on the phone = %+v, want it read", stored)
	}
}

func TestSomebodyElsesReadReceiptLeavesTheUnreadCount(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", nowUTC()); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.SetChatUnreadCount(chat.String(), 3); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}

	a.handleReceiptPersistenceEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		MessageIDs:    []types.MessageID{"outgoing-1"},
		Type:          types.ReceiptTypeRead,
	})

	stored, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if stored.UnreadCount != 3 {
		t.Fatalf("chat after somebody read our message = %+v, want its 3 unread kept", stored)
	}
}
