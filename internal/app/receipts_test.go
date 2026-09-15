package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func seedReceiptMessage(t *testing.T, a *App, chat types.JID, id, sender string, fromMe bool) {
	t.Helper()
	base := time.Now().UTC()
	if err := a.db.UpsertChat(chat.String(), "group", "Test chat", base); err != nil {
		t.Fatalf("upsert chat: %v", err)
	}
	msg := storeUpsertMessage(chat.String(), id, base, "hello")
	msg.SenderJID = sender
	msg.FromMe = fromMe
	if err := a.db.UpsertMessage(msg); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

func markReadCalls(f *fakeWA) []fakeMarkReadReceiptCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMarkReadReceiptCall(nil), f.markReadReceiptCalls...)
}

func TestMarkMessagesReadResolvesGroupSenderFromStore(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "120363000000000000", Server: types.GroupServer}
	seedReceiptMessage(t, a, chat, "MSG1", "15551234567@s.whatsapp.net", false)

	receipt, err := a.MarkMessagesRead(context.Background(), chat, []string{"MSG1"}, types.JID{})
	if err != nil {
		t.Fatalf("MarkMessagesRead: %v", err)
	}
	if receipt != types.ReceiptTypeRead {
		t.Fatalf("receipt = %q, want %q", receipt, types.ReceiptTypeRead)
	}
	calls := markReadCalls(f)
	if len(calls) != 1 {
		t.Fatalf("MarkRead calls = %d, want 1", len(calls))
	}
	if calls[0].chat.String() != chat.String() {
		t.Fatalf("chat = %s, want %s", calls[0].chat, chat)
	}
	if calls[0].sender.String() != "15551234567@s.whatsapp.net" {
		t.Fatalf("sender = %s, want the stored participant", calls[0].sender)
	}
	if len(calls[0].ids) != 1 || calls[0].ids[0] != "MSG1" {
		t.Fatalf("ids = %v, want [MSG1]", calls[0].ids)
	}
}

func TestMarkMessagesReadRejectsUnknownGroupMessageWithoutSender(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "120363000000000000", Server: types.GroupServer}
	_, err := a.MarkMessagesRead(context.Background(), chat, []string{"MISSING"}, types.JID{})
	if err == nil || !strings.Contains(err.Error(), "--sender") {
		t.Fatalf("error = %v, want a hint to pass --sender", err)
	}
}

func TestMarkMessagesReadUsesExplicitSenderAndRejectsEmptyIDs(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "120363000000000000", Server: types.GroupServer}
	sender := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	// Unsynced received messages are accepted when the sender is explicit.
	if _, err := a.MarkMessagesRead(context.Background(), chat, []string{"A", " B "}, sender); err != nil {
		t.Fatalf("MarkMessagesRead: %v", err)
	}
	calls := markReadCalls(f)
	if len(calls) != 1 || len(calls[0].ids) != 2 || calls[0].ids[1] != "B" {
		t.Fatalf("calls = %+v, want one call with ids [A B]", calls)
	}
	if _, err := a.MarkMessagesRead(context.Background(), chat, []string{" "}, sender); err == nil {
		t.Fatalf("expected an error for no ids")
	}
}

func TestMarkMessagesReadRejectsOwnMessageInDirectChat(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	seedReceiptMessage(t, a, chat, "THEIRS", chat.String(), false)
	seedReceiptMessage(t, a, chat, "MINE", "15550000000@s.whatsapp.net", true)

	_, err := a.MarkMessagesRead(context.Background(), chat, []string{"THEIRS", "MINE"}, types.JID{})
	if err == nil || !strings.Contains(err.Error(), "MINE was sent by you") {
		t.Fatalf("error = %v, want own-message rejection", err)
	}
	if calls := markReadCalls(f); len(calls) != 0 {
		t.Fatalf("MarkRead calls = %d, want none", len(calls))
	}
	// A direct-chat message that is not in the store is still accepted.
	if _, err := a.MarkMessagesRead(context.Background(), chat, []string{"THEIRS", "UNSYNCED"}, types.JID{}); err != nil {
		t.Fatalf("MarkMessagesRead: %v", err)
	}
	if calls := markReadCalls(f); len(calls) != 1 || len(calls[0].ids) != 2 {
		t.Fatalf("calls = %+v, want one call with both ids", calls)
	}
}

func TestMarkMessagesReadRejectsOwnMessageInGroupWithExplicitSender(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "120363000000000000", Server: types.GroupServer}
	sender := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	seedReceiptMessage(t, a, chat, "MINE", "15550000000@s.whatsapp.net", true)

	_, err := a.MarkMessagesRead(context.Background(), chat, []string{"MINE"}, sender)
	if err == nil || !strings.Contains(err.Error(), "sent by you") {
		t.Fatalf("error = %v, want own-message rejection", err)
	}
	if calls := markReadCalls(f); len(calls) != 0 {
		t.Fatalf("MarkRead calls = %d, want none", len(calls))
	}
}

func TestMarkMessagesReadReportsReadSelfReceipt(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.markReadReceiptType = types.ReceiptTypeReadSelf
	a.wa = f
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	receipt, err := a.MarkMessagesRead(context.Background(), chat, []string{"MSG1"}, types.JID{})
	if err != nil {
		t.Fatalf("MarkMessagesRead: %v", err)
	}
	if receipt != types.ReceiptTypeReadSelf {
		t.Fatalf("receipt = %q, want %q", receipt, types.ReceiptTypeReadSelf)
	}
}

func TestMarkMessagesReadRejectsOwnMessageStoredUnderChatAlias(t *testing.T) {
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	lid := types.JID{User: "99887766554433", Server: types.HiddenUserServer}
	cases := []struct {
		name   string
		stored types.JID
		chat   types.JID
	}{
		{name: "stored under phone number, chat given as LID", stored: pn, chat: lid},
		{name: "stored under LID, chat given as phone number", stored: lid, chat: pn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			f.lids[lid] = pn
			a.wa = f
			seedReceiptMessage(t, a, tc.stored, "MINE", "15550000000@s.whatsapp.net", true)

			_, err := a.MarkMessagesRead(context.Background(), tc.chat, []string{"MINE"}, types.JID{})
			if err == nil || !strings.Contains(err.Error(), "sent by you") {
				t.Fatalf("error = %v, want own-message rejection through the chat alias", err)
			}
			if calls := markReadCalls(f); len(calls) != 0 {
				t.Fatalf("MarkRead calls = %d, want none", len(calls))
			}
		})
	}
}

func TestMarkMessagesReadPropagatesStoreErrors(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	a.db.Close()

	_, err := a.MarkMessagesRead(context.Background(), chat, []string{"MSG1"}, types.JID{})
	if err == nil || !strings.Contains(err.Error(), "look up message MSG1") {
		t.Fatalf("error = %v, want the store error propagated", err)
	}
	if calls := markReadCalls(f); len(calls) != 0 {
		t.Fatalf("MarkRead calls = %d, want none on a store failure", len(calls))
	}
}
