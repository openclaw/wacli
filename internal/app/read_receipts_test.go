package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func seedReceiptChat(t *testing.T, a *App, chat types.JID, kind string, msgs []store.UpsertMessageParams, unreadCount int) {
	t.Helper()
	base := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), kind, "Chat", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for i, m := range msgs {
		m.ChatJID = chat.String()
		m.Timestamp = base.Add(time.Duration(i) * time.Minute)
		m.Text = "text " + m.MsgID
		if err := a.db.UpsertMessage(m); err != nil {
			t.Fatalf("UpsertMessage %s: %v", m.MsgID, err)
		}
	}
	if err := a.db.SetChatUnreadCount(chat.String(), unreadCount); err != nil {
		t.Fatalf("SetChatUnreadCount: %v", err)
	}
}

func readReceiptCallsOf(f *fakeWA) []fakeReadReceiptCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeReadReceiptCall(nil), f.readReceiptCalls...)
}

func TestMarkChatReadWithReceiptsCoversUnreadIncomingMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{
		{MsgID: "in-1", SenderJID: chat.String()},
		{MsgID: "in-2", SenderJID: chat.String()},
		{MsgID: "out-1", FromMe: true},
		{MsgID: "in-3", SenderJID: chat.String()},
	}, 2)

	n, receipt, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil {
		t.Fatalf("MarkChatReadWithReceipts: %v", err)
	}
	if n != 2 || receipt != wa.ReadReceiptUnknown {
		t.Fatalf("MarkChatReadWithReceipts = %d, %q; want 2 dispatched messages with uncertain sender notification", n, receipt)
	}
	calls := readReceiptCallsOf(f)
	if len(calls) != 1 {
		t.Fatalf("MarkRead calls = %+v, want 1", calls)
	}
	if !slices.Equal(calls[0].ids, []types.MessageID{"in-2", "in-3"}) {
		t.Fatalf("ids = %v, want the two newest incoming messages", calls[0].ids)
	}
	if calls[0].chat != chat || !calls[0].sender.IsEmpty() || calls[0].addressing != "" {
		t.Fatalf("call = %+v, want chat %s without sender or addressing mode", calls[0], chat)
	}
	if calls[0].timestamp.IsZero() {
		t.Fatal("receipt timestamp is zero")
	}
}

func TestMarkChatReadWithReceiptsBatchesGroupMessagesBySender(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	group := types.JID{User: "120363000000000001", Server: types.GroupServer}
	alice := types.JID{User: "111", Server: types.DefaultUserServer}
	bob := types.JID{User: "222", Server: types.DefaultUserServer}
	f.groups[group] = &types.GroupInfo{JID: group, AddressingMode: types.AddressingModeLID}
	seedReceiptChat(t, a, group, "group", []store.UpsertMessageParams{
		{MsgID: "a-1", SenderJID: alice.String()},
		{MsgID: "b-1", SenderJID: bob.String()},
		{MsgID: "me-1", FromMe: true},
		{MsgID: "a-2", SenderJID: alice.String()},
	}, 3)

	n, _, err := a.MarkChatReadWithReceipts(context.Background(), group)
	if err != nil {
		t.Fatalf("MarkChatReadWithReceipts: %v", err)
	}
	if n != 3 {
		t.Fatalf("receipts = %d, want 3", n)
	}
	calls := readReceiptCallsOf(f)
	if len(calls) != 2 {
		t.Fatalf("MarkRead calls = %+v, want one per sender", calls)
	}
	want := []struct {
		sender types.JID
		ids    []types.MessageID
	}{
		{sender: alice, ids: []types.MessageID{"a-1", "a-2"}},
		{sender: bob, ids: []types.MessageID{"b-1"}},
	}
	for i, w := range want {
		got := calls[i]
		if got.chat != group || got.sender != w.sender || !slices.Equal(got.ids, w.ids) {
			t.Fatalf("call %d = %+v, want chat %s sender %s ids %v", i, got, group, w.sender, w.ids)
		}
		if got.addressing != types.AddressingModeLID {
			t.Fatalf("call %d addressing = %q, want the group's LID addressing mode", i, got.addressing)
		}
	}
}

func TestMarkChatReadWithReceiptsSkipsChatsWithoutUnreadMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{{MsgID: "in-1", SenderJID: chat.String()}}, 0)
	unknown := types.JID{User: "789", Server: types.DefaultUserServer}

	if _, _, err := a.MarkChatReadWithReceipts(context.Background(), unknown); err == nil {
		t.Fatal("unknown chat accepted")
	}
	for _, target := range []types.JID{chat} {
		n, receipt, err := a.MarkChatReadWithReceipts(context.Background(), target)
		if err != nil || n != 0 || receipt != "" {
			t.Fatalf("MarkChatReadWithReceipts(%s) = %d, %q, %v; want nothing sent", target, n, receipt, err)
		}
	}
	if calls := readReceiptCallsOf(f); len(calls) != 0 {
		t.Fatalf("MarkRead calls = %+v, want none", calls)
	}
}

func TestMarkChatReadWithReceiptsCoversLatestMessageForUnreadMarker(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{
		{MsgID: "in-1", SenderJID: chat.String()},
		{MsgID: "in-2", SenderJID: chat.String()},
	}, 0)
	if err := a.db.SetChatUnread(chat.String(), true); err != nil {
		t.Fatalf("SetChatUnread: %v", err)
	}

	n, _, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil || n != 1 {
		t.Fatalf("MarkChatReadWithReceipts = %d, %v; want 1, nil", n, err)
	}
	if calls := readReceiptCallsOf(f); len(calls) != 1 || !slices.Equal(calls[0].ids, []types.MessageID{"in-2"}) {
		t.Fatalf("MarkRead calls = %+v, want only the latest message", calls)
	}
}

func TestMarkChatReadWithReceiptsCapsLargeUnreadCounts(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	msgs := make([]store.UpsertMessageParams, 0, maxReadReceiptMessages+20)
	for i := range maxReadReceiptMessages + 20 {
		msgs = append(msgs, store.UpsertMessageParams{MsgID: fmt.Sprintf("in-%03d", i), SenderJID: chat.String()})
	}
	seedReceiptChat(t, a, chat, "dm", msgs, len(msgs))

	n, _, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil {
		t.Fatalf("MarkChatReadWithReceipts: %v", err)
	}
	if n != maxReadReceiptMessages {
		t.Fatalf("receipts = %d, want the %d-message cap", n, maxReadReceiptMessages)
	}
	calls := readReceiptCallsOf(f)
	newest := types.MessageID("in-000")
	if len(calls) != 1 || len(calls[0].ids) != maxReadReceiptMessages || calls[0].ids[0] != newest {
		t.Fatalf("MarkRead calls = %d, want one capped batch starting at %s", len(calls), newest)
	}
}

func TestMarkChatReadWithReceiptsReportsSendErrors(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.readReceiptErr = errors.New("socket closed")
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{{MsgID: "in-1", SenderJID: chat.String()}}, 1)

	n, _, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err == nil || !strings.Contains(err.Error(), "socket closed") || n != 0 {
		t.Fatalf("MarkChatReadWithReceipts = %d, %v; want 0 and the send error", n, err)
	}
}

func TestMarkChatReadWithReceiptsLooksPastReactions(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	// The two newest incoming rows are reactions: counting them against the
	// unread count would acknowledge nothing at all.
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{
		{MsgID: "in-1", SenderJID: chat.String()},
		{MsgID: "in-2", SenderJID: chat.String()},
		{MsgID: "react-1", SenderJID: chat.String(), ReactionToID: "in-2", ReactionEmoji: "x"},
		{MsgID: "react-2", SenderJID: chat.String(), ReactionToID: "in-1", ReactionEmoji: "x"},
	}, 1)

	n, _, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil || n != 1 {
		t.Fatalf("MarkChatReadWithReceipts = %d, %v; want the newest real message acknowledged", n, err)
	}
	calls := readReceiptCallsOf(f)
	if len(calls) != 1 || !slices.Equal(calls[0].ids, []types.MessageID{"in-2"}) {
		t.Fatalf("MarkRead calls = %+v, want only in-2", calls)
	}
}

func TestMarkChatReadWithReceiptsWidensTheSearchPastManyReactions(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	msgs := []store.UpsertMessageParams{
		{MsgID: "in-1", SenderJID: chat.String()},
		{MsgID: "in-2", SenderJID: chat.String()},
	}
	for i := range 40 {
		msgs = append(msgs, store.UpsertMessageParams{
			MsgID:         fmt.Sprintf("react-%02d", i),
			SenderJID:     chat.String(),
			ReactionToID:  "in-2",
			ReactionEmoji: "x",
		})
	}
	seedReceiptChat(t, a, chat, "dm", msgs, 2)

	n, _, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil || n != 2 {
		t.Fatalf("MarkChatReadWithReceipts = %d, %v; want both real messages acknowledged", n, err)
	}
	calls := readReceiptCallsOf(f)
	if len(calls) != 1 || !slices.Equal(calls[0].ids, []types.MessageID{"in-1", "in-2"}) {
		t.Fatalf("MarkRead calls = %+v, want the two real messages", calls)
	}
}

func TestMarkChatReadWithReceiptsReportsHiddenReceipts(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	// WhatsApp downgrades the receipt when the account hides read receipts, so
	// the sender sees nothing; the caller has to be able to tell.
	f.readReceiptType = types.ReceiptTypeReadSelf
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{{MsgID: "in-1", SenderJID: chat.String()}}, 1)

	n, receipt, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil || n != 1 || receipt != types.ReceiptTypeReadSelf {
		t.Fatalf("MarkChatReadWithReceipts = %d, %q, %v; want 1 message sent as read-self", n, receipt, err)
	}
}

func TestReadReceiptsCapLeavesUnsentMessagesUnread(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "456", Server: types.DefaultUserServer}
	var msgs []store.UpsertMessageParams
	for i := range 120 {
		msgs = append(msgs, store.UpsertMessageParams{MsgID: fmt.Sprintf("in-%03d", i), SenderJID: chat.String()})
	}
	seedReceiptChat(t, a, chat, "dm", msgs, len(msgs))
	n, _, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil || n != 100 {
		t.Fatalf("first batch = %d, %v", n, err)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.UnreadCount != 20 || !c.Unread {
		t.Fatalf("after capped batch = %+v, want 20 remaining", c)
	}
	n, _, err = a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil || n != 20 {
		t.Fatalf("second batch = %d, %v", n, err)
	}
	calls := readReceiptCallsOf(f)
	seen := map[string]bool{}
	for _, call := range calls {
		for _, id := range call.ids {
			if seen[string(id)] {
				t.Errorf("duplicate receipt for %s", id)
			}
			seen[string(id)] = true
		}
	}
	if len(seen) != 120 {
		t.Fatalf("acknowledged %d distinct messages", len(seen))
	}
}

func TestReadReceiptsRejectMissingGroupSenderBeforeSending(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.JID{User: "123", Server: types.GroupServer}
	f.groups[chat] = &types.GroupInfo{JID: chat}
	seedReceiptChat(t, a, chat, "group", []store.UpsertMessageParams{{MsgID: "missing-sender"}, {MsgID: "valid", SenderJID: "456@s.whatsapp.net"}}, 2)
	if _, _, err := a.MarkChatReadWithReceipts(context.Background(), chat); err == nil {
		t.Fatal("missing sender accepted")
	}
	if calls := readReceiptCallsOf(f); len(calls) != 0 {
		t.Fatalf("partial dispatch before validating all senders: %+v", calls)
	}
}

func TestReceiptModePreservesMessagesArrivingDuringDispatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{{MsgID: "old", SenderJID: chat.String()}}, 1)
	f.appStateFetchErr = errors.New("recovery unavailable")
	f.onReadReceipt = func(int) (types.ReceiptType, error) {
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "new", SenderJID: chat.String(), Text: "new", Timestamp: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)}); err != nil {
			return "", err
		}
		return wa.ReadReceiptUnknown, a.db.IncrementChatUnread(chat.String())
	}
	if _, _, err := a.MarkChatReadWithReceipts(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.UnreadCount != 1 || !c.Unread {
		t.Fatalf("new arrival lost: %+v", c)
	}
	if len(f.appStateFetches) != 0 {
		t.Fatal("receipt mode used app-state recovery")
	}
}

func TestReceiptModeKeepsMixedBatchOutcomesAndPartialFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.NewJID("123", types.GroupServer)
			f.groups[chat] = &types.GroupInfo{JID: chat}
			seedReceiptChat(t, a, chat, "group", []store.UpsertMessageParams{{MsgID: "a", SenderJID: "111@s.whatsapp.net"}, {MsgID: "b", SenderJID: "222@s.whatsapp.net"}}, 2)
			f.onReadReceipt = func(i int) (types.ReceiptType, error) {
				if i == 0 {
					return wa.ReadReceiptUnknown, nil
				}
				if fail {
					return "", errors.New("second batch failed")
				}
				return types.ReceiptTypeReadSelf, nil
			}
			n, kind, err := a.MarkChatReadWithReceipts(context.Background(), chat)
			c, dbErr := a.db.GetChat(chat.String())
			if dbErr != nil {
				t.Fatal(dbErr)
			}
			if fail {
				if err == nil || n != 1 || c.UnreadCount != 2 {
					t.Fatalf("partial=%d %v state=%+v", n, err, c)
				}
			} else if err != nil || n != 2 || kind != wa.ReadReceiptUnknown || c.UnreadCount != 0 {
				t.Fatalf("mixed=%d %s %v state=%+v", n, kind, err, c)
			}
		})
	}
}

func TestReceiptModeFailsBeforeDispatchWhenHistoryIsIncomplete(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{{MsgID: "known", SenderJID: chat.String()}}, 2)
	if _, _, err := a.MarkChatReadWithReceipts(context.Background(), chat); err == nil {
		t.Fatal("incomplete unread history accepted")
	}
	if len(readReceiptCallsOf(f)) != 0 {
		t.Fatal("receipt sent before history validation")
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.UnreadCount != 2 {
		t.Fatal("unknown unread messages cleared")
	}
}

func TestReceiptModeClearsEmptyMarkerWithoutNetwork(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	seedReceiptChat(t, a, chat, "dm", nil, 0)
	if err := a.db.SetChatUnread(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	n, _, err := a.MarkChatReadWithReceipts(context.Background(), chat)
	if err != nil || n != 0 || len(readReceiptCallsOf(f)) != 0 {
		t.Fatalf("empty marker: %d %v", n, err)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.Unread {
		t.Fatal("empty marker not cleared")
	}
}
