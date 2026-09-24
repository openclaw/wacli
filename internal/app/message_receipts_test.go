package app

import (
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestReceiptStatusNamesTheStatesAMessageTravelsThrough(t *testing.T) {
	cases := []struct {
		kind types.ReceiptType
		want string
		ok   bool
	}{
		// WhatsApp spells delivery as the empty type.
		{types.ReceiptTypeDelivered, "delivered", true},
		{types.ReceiptTypeRead, "read", true},
		{types.ReceiptTypePlayed, "played", true},
		{types.ReceiptTypeReadSelf, "", false},
		{types.ReceiptTypeSender, "", false},
		{types.ReceiptTypeRetry, "", false},
	}
	for _, tc := range cases {
		got, ok := receiptStatus(tc.kind)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("receiptStatus(%q) = %q, %t; want %q, %t", tc.kind, got, ok, tc.want, tc.ok)
		}
	}
}

func TestOutgoingReceiptsAreStoredPerRecipient(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	group := types.JID{User: "999", Server: types.GroupServer}
	first := types.JID{User: "111", Server: types.DefaultUserServer}
	second := types.JID{User: "222", Server: types.DefaultUserServer}
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(group.String(), "group", "Friends", at); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID: group.String(), MsgID: "g1", Timestamp: at, FromMe: true, Text: "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	a.handleOutgoingReceiptEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: group, Sender: first, IsGroup: true},
		MessageIDs:    []types.MessageID{"g1"},
		Timestamp:     at.Add(time.Minute),
		Type:          types.ReceiptTypeDelivered,
	})
	a.handleOutgoingReceiptEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: group, Sender: second, IsGroup: true},
		MessageIDs:    []types.MessageID{"g1"},
		Timestamp:     at.Add(2 * time.Minute),
		Type:          types.ReceiptTypeRead,
	})

	counts, err := a.db.MessageReceipts(group.String(), "g1")
	if err != nil {
		t.Fatalf("MessageReceipts: %v", err)
	}
	if counts.Delivered != 2 || counts.Read != 1 {
		t.Fatalf("counts = %+v, want two recipients with it and one read", counts)
	}
}

func TestDirectReceiptWithoutAParticipantCountsTheChat(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", at); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID: chat.String(), MsgID: "m1", Timestamp: at, FromMe: true, Text: "hello",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	a.handleOutgoingReceiptEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		MessageIDs:    []types.MessageID{"m1"},
		Timestamp:     at.Add(time.Minute),
		Type:          types.ReceiptTypeRead,
	})

	counts, err := a.db.MessageReceipts(chat.String(), "m1")
	if err != nil {
		t.Fatalf("MessageReceipts: %v", err)
	}
	if counts.Delivered != 1 || counts.Read != 1 {
		t.Fatalf("counts = %+v, want the one recipient counted as read", counts)
	}
}

func TestOurOwnChatCountsOurOwnReceipts(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	me, err := types.ParseJID(f.LinkedJID())
	if err != nil {
		t.Fatalf("ParseJID: %v", err)
	}
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(me.String(), "dm", "You", at); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID: me.String(), MsgID: "note-1", Timestamp: at, FromMe: true, Text: "a note",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	// In our own chat every device is ours, so our own receipt is the delivery.
	a.handleOutgoingReceiptEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: me, IsFromMe: true},
		MessageIDs:    []types.MessageID{"note-1"},
		Timestamp:     at.Add(time.Minute),
		Type:          types.ReceiptTypeRead,
	})

	counts, err := a.db.MessageReceipts(me.String(), "note-1")
	if err != nil {
		t.Fatalf("MessageReceipts: %v", err)
	}
	if counts.Delivered != 1 || counts.Read != 1 {
		t.Fatalf("counts = %+v, want our own device counted as having read it", counts)
	}
}

func TestOurOwnReceiptsAreNotDeliveryReports(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Alice", at); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}

	// Our phone reporting that we read their message says nothing about ours.
	a.handleOutgoingReceiptEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: true},
		MessageIDs:    []types.MessageID{"incoming-1"},
		Timestamp:     at,
		Type:          types.ReceiptTypeRead,
	})
	// Neither does protocol bookkeeping.
	a.handleOutgoingReceiptEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat},
		MessageIDs:    []types.MessageID{"m1"},
		Timestamp:     at,
		Type:          types.ReceiptTypeRetry,
	})

	for _, id := range []string{"incoming-1", "m1"} {
		counts, err := a.db.MessageReceipts(chat.String(), id)
		if err != nil {
			t.Fatalf("MessageReceipts %s: %v", id, err)
		}
		if counts.Delivered != 0 || counts.Read != 0 {
			t.Fatalf("%s carries %+v, want nothing stored", id, counts)
		}
	}
}
