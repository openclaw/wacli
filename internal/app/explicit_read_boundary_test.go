package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

func seedExplicitReadBoundary(t *testing.T, a *App, group bool) (types.JID, time.Time) {
	t.Helper()
	chat := types.NewJID("456", types.DefaultUserServer)
	kind := "dm"
	if group {
		chat = types.NewJID("123", types.GroupServer)
		kind = "group"
	}
	seedReceiptChat(t, a, chat, kind, nil, 0)
	at := time.Unix(1700000000, 0)
	// Deliberately keep A and B in the same second. Their stable row order,
	// rather than timestamp alone, decides which message was handled.
	for i, id := range []string{"earlier", "handled", "newer"} {
		sender := types.NewJID("111", types.DefaultUserServer).String()
		if i == 1 {
			sender = types.NewJID("222", types.DefaultUserServer).String()
		}
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, SenderJID: sender, Timestamp: at, Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.db.SetChatUnreadCount(chat.String(), 3); err != nil {
		t.Fatal(err)
	}
	return chat, at
}

func TestExplicitReadBoundaryPreservesNewerSameSecondMessages(t *testing.T) {
	for _, receipts := range []bool{false, true} {
		t.Run(map[bool]string{false: "app-state", true: "receipts"}[receipts], func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat, at := seedExplicitReadBoundary(t, a, false)
			remaining := 1
			arrival := func(id string) {
				t.Helper()
				if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, SenderJID: chat.String(), Timestamp: at, Text: id}); err != nil {
					t.Fatal(err)
				}
				if err := a.db.IncrementChatUnread(chat.String()); err != nil {
					t.Fatal(err)
				}
				remaining++
			}
			if receipts {
				f.onReadReceipt = func(int) (types.ReceiptType, error) {
					arrival("during-send")
					return types.ReceiptTypeReadSelf, nil
				}
				n, _, err := a.MarkChatReadWithReceiptsThrough(context.Background(), chat, "handled")
				if err != nil || n != 2 {
					t.Fatalf("receipts = %d, %v", n, err)
				}
				calls := readReceiptCallsOf(f)
				if len(calls) != 1 || !slices.Equal(calls[0].ids, []types.MessageID{"earlier", "handled"}) {
					t.Fatalf("wrong receipt selection: %+v", calls)
				}
			} else {
				// Recovery must not extend the already chosen boundary.
				f.appStateFetchErrs = []error{errors.Join(errors.New("delta mismatch"), appstate.ErrMismatchingLTHash), nil}
				f.appStateFetchEvent = func(_ string, fullSync, _ bool) any {
					if fullSync {
						arrival("during-recovery")
					}
					return nil
				}
				f.markReadBeforeApply = func() { arrival("during-send") }
				if err := a.MarkChatReadThrough(context.Background(), chat, "handled"); err != nil {
					t.Fatal(err)
				}
				if len(f.markReadCalls) != 1 || !f.markReadCalls[0].lastMsgTS.Equal(at) || f.markReadCalls[0].lastMsgKey.GetID() != "handled" || f.markReadCalls[0].lastMsgKey.GetFromMe() {
					t.Fatalf("wrong app-state range: %+v", f.markReadCalls)
				}
			}
			c, err := a.db.GetChat(chat.String())
			if err != nil || !c.Unread || c.UnreadCount != remaining {
				t.Fatalf("newer message lost: %+v, %v", c, err)
			}
		})
	}
}

func TestExplicitReadBoundaryRejectsInvalidAnchorsBeforeUpdates(t *testing.T) {
	for _, receipts := range []bool{false, true} {
		a := newTestApp(t)
		f := newFakeWA()
		a.wa = f
		chat, at := seedExplicitReadBoundary(t, a, false)
		if err := a.db.UpsertChat("789@s.whatsapp.net", "dm", "Other", at); err != nil {
			t.Fatal(err)
		}
		for _, m := range []store.UpsertMessageParams{
			{ChatJID: "789@s.whatsapp.net", MsgID: "foreign"},
			{ChatJID: chat.String(), MsgID: "outgoing", FromMe: true, SkipOwnReadMark: true}, // a fixture, not a reply that marks the chat read
			{ChatJID: chat.String(), MsgID: "revoked", Revoked: true},
			{ChatJID: chat.String(), MsgID: "deleted", DeletedForMe: true},
			{ChatJID: chat.String(), MsgID: "reaction", ReactionToID: "handled", ReactionEmoji: "ok"},
		} {
			m.Timestamp, m.Text = at, m.MsgID
			if err := a.db.UpsertMessage(m); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"", " ", " handled", "missing", "foreign", "outgoing", "revoked", "deleted", "reaction"} {
			var err error
			if receipts {
				_, _, err = a.MarkChatReadWithReceiptsThrough(context.Background(), chat, id)
			} else {
				err = a.MarkChatReadThrough(context.Background(), chat, id)
			}
			if err == nil {
				t.Errorf("accepted anchor %q (receipts=%t)", id, receipts)
			}
		}
		if len(f.appStateFetches) != 0 || len(f.markReadCalls) != 0 || len(readReceiptCallsOf(f)) != 0 {
			t.Fatal("invalid anchor reached network updates")
		}
		c, err := a.db.GetChat(chat.String())
		if err != nil || c.UnreadCount != 3 || !c.Unread {
			t.Fatalf("invalid anchor changed local unread state: %+v %v", c, err)
		}
	}
}

func TestExplicitReadBoundaryGroupReceiptsRetainPrivacyAndPartialFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat, _ := seedExplicitReadBoundary(t, a, true)
			f.groups[chat] = &types.GroupInfo{JID: chat, AddressingMode: types.AddressingModeLID}
			f.onReadReceipt = func(i int) (types.ReceiptType, error) {
				if fail && i == 1 {
					return "", errors.New("second sender failed")
				}
				return types.ReceiptTypeReadSelf, nil
			}
			n, kind, err := a.MarkChatReadWithReceiptsThrough(context.Background(), chat, "handled")
			c, dbErr := a.db.GetChat(chat.String())
			if dbErr != nil {
				t.Fatal(dbErr)
			}
			if fail {
				if err == nil || n != 1 || c.UnreadCount != 3 {
					t.Fatalf("partial receipt failure = %d %v state=%+v", n, err, c)
				}
			} else if err != nil || n != 2 || kind != types.ReceiptTypeReadSelf || c.UnreadCount != 1 {
				t.Fatalf("bounded private receipts = %d %q %v state=%+v", n, kind, err, c)
			}
			calls := readReceiptCallsOf(f)
			for i, call := range calls {
				wantSender := types.NewJID([]string{"111", "222"}[i], types.DefaultUserServer)
				if call.sender != wantSender || call.chat != chat || call.addressing != types.AddressingModeLID || len(call.ids) != 1 || call.ids[0] == "newer" {
					t.Fatalf("wrong sender/boundary: %+v", calls)
				}
			}
			if len(f.appStateFetches) != 0 || len(f.markReadCalls) != 0 {
				t.Fatal("receipt mode entered app-state recovery")
			}
		})
	}
}

func TestExplicitReadBoundaryReceiptsRespectUnreadSuffixAndCap(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	var msgs []store.UpsertMessageParams
	for i := range 121 {
		msgs = append(msgs, store.UpsertMessageParams{MsgID: fmt.Sprintf("in-%03d", i), SenderJID: chat.String()})
	}
	seedReceiptChat(t, a, chat, "dm", msgs, len(msgs))
	for _, want := range []int{100, 20, 0} {
		n, _, err := a.MarkChatReadWithReceiptsThrough(context.Background(), chat, "in-119")
		if err != nil || n != want {
			t.Fatalf("bounded batch = %d, %v; want %d", n, err, want)
		}
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil || !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("unread suffix lost: %+v %v", c, err)
	}
	seen := map[types.MessageID]bool{}
	for _, call := range readReceiptCallsOf(f) {
		for _, id := range call.ids {
			if seen[id] || id == "in-120" {
				t.Fatalf("duplicate or beyond-boundary receipt: %s", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 120 {
		t.Fatalf("acknowledged %d messages, want 120", len(seen))
	}
}

func TestExplicitReadBoundaryGroupPatchUsesAnchorSender(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat, _ := seedExplicitReadBoundary(t, a, true)
	if err := a.MarkChatReadThrough(context.Background(), chat, "handled"); err != nil {
		t.Fatal(err)
	}
	if len(f.markReadCalls) != 1 || f.markReadCalls[0].lastMsgKey.GetParticipant() != "222@s.whatsapp.net" || f.markReadCalls[0].lastMsgKey.GetFromMe() {
		t.Fatalf("wrong group anchor key: %+v", f.markReadCalls)
	}
}

func TestExplicitReadBoundaryPreservesMarkerBeyondAnchor(t *testing.T) {
	for _, receipts := range []bool{false, true} {
		t.Run(fmt.Sprint(receipts), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat, _ := seedExplicitReadBoundary(t, a, false)
			if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatUnread(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			if receipts {
				n, _, err := a.MarkChatReadWithReceiptsThrough(context.Background(), chat, "handled")
				if err != nil || n != 0 || len(readReceiptCallsOf(f)) != 0 {
					t.Fatalf("nothing before anchor is selected: %d %v", n, err)
				}
			} else if err := a.MarkChatReadThrough(context.Background(), chat, "handled"); err != nil {
				t.Fatal(err)
			}
			c, err := a.db.GetChat(chat.String())
			if err != nil || !c.Unread || c.UnreadCount != 0 {
				t.Fatalf("newer marker-only message lost: %+v %v", c, err)
			}
		})
	}
}
