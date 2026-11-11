package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func seedExactChat(t *testing.T, a *App, chat types.JID, seconds map[string]int, unread int) time.Time {
	t.Helper()
	base := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Chat", base); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(seconds))
	for id := range seconds {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(x, y string) int { return seconds[x] - seconds[y] })
	for _, id := range ids {
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, SenderJID: chat.String(), Timestamp: base.Add(time.Duration(seconds[id]) * time.Second), Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.db.SetChatUnreadCount(chat.String(), unread); err != nil {
		t.Fatal(err)
	}
	return base
}

func emittedIDs(f *fakeWA) []types.MessageID {
	var ids []types.MessageID
	for _, call := range readReceiptCallsOf(f) {
		ids = append(ids, call.ids...)
	}
	return ids
}

func liveArrival(t *testing.T, a *App, chat types.JID, base time.Time, id string, second int) {
	t.Helper()
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, SenderJID: chat.String(), Timestamp: base.Add(time.Duration(second) * time.Second), Text: id}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.IncrementChatUnread(chat.String()); err != nil {
		t.Fatal(err)
	}
}

func unreadIDs(t *testing.T, a *App, chat types.JID) []string {
	t.Helper()
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := a.db.ReadReceiptCandidates(chat.String(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range msgs {
		ids = append(ids, m.MsgID)
	}
	if len(ids) != c.UnreadCount {
		t.Fatalf("unread selection %v disagrees with count %d", ids, c.UnreadCount)
	}
	return ids
}

// Arrivals while receipts are in flight, at every position relative to the
// acknowledged messages. Exactly the requested IDs are emitted, and nothing
// that arrived is lost from the local unread state.
func TestExactReceiptsEmitOnlyRequestedIDsWhileMessagesArrive(t *testing.T) {
	for _, tc := range []struct {
		name        string
		second      int
		wantSettled bool
		wantUnread  []string
	}{
		{"older than the acknowledged messages", 20, false, []string{"a", "x", "b"}},
		{"in the last acknowledged second", 30, true, []string{"x"}},
		{"newer", 40, true, []string{"x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.NewJID("456", types.DefaultUserServer)
			base := seedExactChat(t, a, chat, map[string]int{"read": 0, "a": 10, "b": 30}, 2)
			f.onReadReceipt = func(int) (types.ReceiptType, error) {
				liveArrival(t, a, chat, base, "x", tc.second)
				return wa.ReadReceiptUnknown, nil
			}
			n, receipt, settled, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, []string{"a", "b"})
			if err != nil {
				t.Fatal(err)
			}
			if n != 2 || receipt != wa.ReadReceiptUnknown || settled != tc.wantSettled {
				t.Fatalf("result = %d %q settled=%t, want 2 unknown settled=%t", n, receipt, settled, tc.wantSettled)
			}
			if got := emittedIDs(f); !slices.Equal(got, []types.MessageID{"a", "b"}) {
				t.Fatalf("emitted %v, want exactly a,b", got)
			}
			if got := unreadIDs(t, a, chat); !slices.Equal(got, tc.wantUnread) {
				t.Fatalf("unread = %v, want %v", got, tc.wantUnread)
			}
		})
	}
}

// The v0.26.0 anchor path on the same race, for contrast: the receipt count
// still matches, but the older arrival is cleared locally without a receipt.
func TestAnchorReceiptsClearAnOlderArrivalDuringDispatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	base := seedExactChat(t, a, chat, map[string]int{"a": 10, "b": 30}, 2)
	f.onReadReceipt = func(int) (types.ReceiptType, error) {
		liveArrival(t, a, chat, base, "x", 20)
		return wa.ReadReceiptUnknown, nil
	}
	n, _, err := a.MarkChatReadWithReceiptsThrough(context.Background(), chat, "b")
	if err != nil || n != 2 {
		t.Fatalf("anchor = %d, %v", n, err)
	}
	if got := unreadIDs(t, a, chat); len(got) != 0 {
		t.Fatalf("unread = %v; the contrast case no longer reproduces", got)
	}
}

func TestExactReceiptsRefuseBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
		prep func(*App, types.JID)
	}{
		{"skips an unread message", []string{"b"}, nil},
		{"not unread locally", []string{"read"}, nil},
		{"after an own reply cleared the count", []string{"a", "b"}, func(a *App, chat types.JID) {
			_ = a.db.SetChatUnreadCount(chat.String(), 0)
		}},
		{"a contentless message among them", []string{"a", "b"}, func(a *App, chat types.JID) {
			_ = a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "p", SenderJID: chat.String(), Timestamp: time.Date(2024, 3, 1, 12, 0, 20, 0, time.UTC), DisplayText: "(message)"})
		}},
		{"duplicate", []string{"a", "a"}, nil},
		{"none", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.NewJID("456", types.DefaultUserServer)
			seedExactChat(t, a, chat, map[string]int{"read": 0, "a": 10, "b": 30}, 2)
			if tc.prep != nil {
				tc.prep(a, chat)
			}
			if _, _, _, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, tc.ids); err == nil {
				t.Fatal("accepted")
			}
			if calls := readReceiptCallsOf(f); len(calls) != 0 {
				t.Fatalf("receipts sent: %+v", calls)
			}
		})
	}
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.NewJID("456", types.DefaultUserServer)
	seedExactChat(t, a, chat, map[string]int{"a": 10}, 1)
	ids := make([]string, maxReadReceiptMessages+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}
	if _, _, _, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, ids); err == nil {
		t.Fatal("more than the cap accepted")
	}
}

func TestExactReceiptsKeepUnreadWhenDispatchFails(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	f.readReceiptErr = errors.New("not connected")
	chat := types.NewJID("456", types.DefaultUserServer)
	seedExactChat(t, a, chat, map[string]int{"a": 10, "b": 30}, 2)
	if _, _, settled, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, []string{"a", "b"}); err == nil || settled {
		t.Fatalf("failed dispatch = settled %t, %v", settled, err)
	}
	if got := unreadIDs(t, a, chat); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("unread after failure = %v", got)
	}
}

// A repeat after an unsettled dispatch must name the still-unread messages
// again; it resends their receipts and then settles.
func TestExactReceiptsRepeatAfterAnUnsettledDispatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	base := seedExactChat(t, a, chat, map[string]int{"a": 10, "b": 30}, 2)
	f.onReadReceipt = func(int) (types.ReceiptType, error) {
		liveArrival(t, a, chat, base, "x", 20)
		f.onReadReceipt = nil
		return wa.ReadReceiptUnknown, nil
	}
	if _, _, settled, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, []string{"a", "b"}); err != nil || settled {
		t.Fatalf("first = settled %t, %v", settled, err)
	}
	// x is unread and unseen: a,b alone are no longer the oldest unread set.
	if _, _, _, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, []string{"a", "b"}); err == nil {
		t.Fatal("acknowledged past the unseen arrival")
	}
	if _, _, settled, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, []string{"a"}); err != nil || !settled {
		t.Fatalf("oldest only = settled %t, %v", settled, err)
	}
	if got := unreadIDs(t, a, chat); !slices.Equal(got, []string{"x", "b"}) {
		t.Fatalf("unread = %v, want x and b", got)
	}
	if got := emittedIDs(f); !slices.Equal(got, []types.MessageID{"a", "b", "a"}) {
		t.Fatalf("emitted %v", got)
	}
}

// Sync stores a live message, then counts it. Selecting between the two sees a
// stale count: the newest-N selection then starts after the oldest unread
// message, and settlement would leave that message read once the count
// lands. Exact receipts wait until an arrival in flight is counted.
func TestExactReceiptsWaitForALiveArrivalToBeCounted(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	base := seedExactChat(t, a, chat, map[string]int{"a": 10}, 1)
	stored := make(chan struct{})
	release := make(chan struct{})
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		var n atomic.Int64
		opts := SyncOptions{afterLiveStore: func() {
			close(stored)
			<-release
		}}
		evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "b", Timestamp: base.Add(30 * time.Second)}, Message: &waE2E.Message{Conversation: proto.String("b")}}
		a.handleLiveSyncMessage(context.Background(), opts, evt, &n, nil, nil)
	}()
	<-stored // b is stored and not yet counted
	done := make(chan error, 1)
	go func() {
		_, _, _, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, []string{"b"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("selected before b was counted: %v, emitted %v", err, emittedIDs(f))
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-handled
	if err := <-done; !errors.Is(err, store.ErrNotOldestUnread) {
		t.Fatalf("err = %v, want a still first", err)
	}
	if got := emittedIDs(f); len(got) != 0 {
		t.Fatalf("emitted %v", got)
	}
	if got := unreadIDs(t, a, chat); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("unread = %v, want a and b", got)
	}
}

// An arrival in flight while settling is waited for as well. Once it is
// counted, a clean newer arrival still settles, and it stays unread.
func TestExactSettlementWaitsForALiveArrivalToBeCounted(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	base := seedExactChat(t, a, chat, map[string]int{"a": 10, "b": 30}, 2)
	release := make(chan struct{})
	handled := make(chan struct{})
	f.onReadReceipt = func(int) (types.ReceiptType, error) {
		stored := make(chan struct{})
		go func() {
			defer close(handled)
			var n atomic.Int64
			opts := SyncOptions{afterLiveStore: func() {
				close(stored)
				<-release
			}}
			evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "x", Timestamp: base.Add(40 * time.Second)}, Message: &waE2E.Message{Conversation: proto.String("x")}}
			a.handleLiveSyncMessage(context.Background(), opts, evt, &n, nil, nil)
		}()
		<-stored // x is stored and not yet counted while receipts are in flight
		time.AfterFunc(100*time.Millisecond, func() { close(release) })
		return wa.ReadReceiptUnknown, nil
	}
	_, _, settled, err := a.MarkChatMessagesReadWithReceipts(context.Background(), chat, []string{"a", "b"})
	<-handled
	if err != nil || !settled {
		t.Fatalf("settled %t, %v; want settled once x was counted", settled, err)
	}
	if got := unreadIDs(t, a, chat); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("unread = %v, want x", got)
	}
}

// A caller that gives up while its request waits for an arrival to be counted
// gets nothing sent, though its message would still be the oldest unread.
func TestExactReceiptsSendNothingOnceTheCallerGaveUp(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	base := seedExactChat(t, a, chat, map[string]int{"a": 10}, 1)
	stored := make(chan struct{})
	release := make(chan struct{})
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		var n atomic.Int64
		opts := SyncOptions{afterLiveStore: func() {
			close(stored)
			<-release
		}}
		evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "b", Timestamp: base.Add(30 * time.Second)}, Message: &waE2E.Message{Conversation: proto.String("b")}}
		a.handleLiveSyncMessage(context.Background(), opts, evt, &n, nil, nil)
	}()
	<-stored // b is stored and not yet counted
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := a.MarkChatMessagesReadWithReceipts(ctx, chat, []string{"a"})
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // the request is waiting
	cancel()
	close(release)
	<-handled
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
	if got := emittedIDs(f); len(got) != 0 {
		t.Fatalf("emitted %v", got)
	}
	if got := unreadIDs(t, a, chat); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("unread = %v, want a and b", got)
	}
}
