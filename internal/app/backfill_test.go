package app

import (
	"context"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBackfillHistoryAddsOlderMessages(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "123", Server: types.DefaultUserServer}
	chatStr := chat.String()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := a.db.UpsertChat(chatStr, "dm", "Alice", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(chatStr, "m2", base.Add(2*time.Second), "newer")); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	f.onDemandHistory = func(lastKnown types.MessageInfo, count int) *events.HistorySync {
		older := &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(chatStr),
				FromMe:    proto.Bool(false),
				ID:        proto.String("m1"),
			},
			MessageTimestamp: proto.Uint64(uint64(base.Add(1 * time.Second).Unix())),
			Message:          &waProto.Message{Conversation: proto.String("older")},
		}
		return &events.HistorySync{
			Data: &waHistorySync.HistorySync{
				SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
				Conversations: []*waHistorySync.Conversation{{
					ID:                       proto.String(chatStr),
					EndOfHistoryTransfer:     proto.Bool(true),
					EndOfHistoryTransferType: waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum(),
					Messages:                 []*waHistorySync.HistorySyncMsg{{Message: older}},
				}},
			},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := a.BackfillHistory(ctx, BackfillOptions{
		ChatJID:        chatStr,
		Count:          50,
		Requests:       1,
		WaitPerRequest: 1 * time.Second,
		IdleExit:       200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("BackfillHistory: %v", err)
	}
	if res.MessagesAdded <= 0 {
		t.Fatalf("expected messages to be added, got %d", res.MessagesAdded)
	}

	oldest, err := a.db.GetOldestMessageInfo(chatStr)
	if err != nil {
		t.Fatalf("GetOldestMessageInfo: %v", err)
	}
	if oldest.MsgID != "m1" {
		t.Fatalf("expected oldest m1, got %q", oldest.MsgID)
	}
}

func storeUpsertMessage(chatJID, id string, ts time.Time, text string) store.UpsertMessageParams {
	return store.UpsertMessageParams{
		ChatJID:    chatJID,
		MsgID:      id,
		SenderJID:  chatJID,
		SenderName: "Alice",
		Timestamp:  ts,
		FromMe:     false,
		Text:       text,
	}
}

func TestFillHistoryMarksCompleteAndBlockedChats(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chatReady := types.JID{User: "123", Server: types.DefaultUserServer}
	chatBlocked := types.JID{User: "456", Server: types.DefaultUserServer}
	base := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)

	if err := a.db.UpsertChat(chatReady.String(), "dm", "Ready", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpsertChat ready: %v", err)
	}
	if err := a.db.UpsertChat(chatBlocked.String(), "dm", "Blocked", base.Add(1*time.Minute)); err != nil {
		t.Fatalf("UpsertChat blocked: %v", err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(chatReady.String(), "m2", base.Add(2*time.Second), "newer")); err != nil {
		t.Fatalf("UpsertMessage ready: %v", err)
	}

	f.onDemandHistory = func(lastKnown types.MessageInfo, count int) *events.HistorySync {
		older := &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(lastKnown.Chat.String()),
				FromMe:    proto.Bool(false),
				ID:        proto.String("m1"),
			},
			MessageTimestamp: proto.Uint64(uint64(base.Add(1 * time.Second).Unix())),
			Message:          &waProto.Message{Conversation: proto.String("older")},
		}
		return &events.HistorySync{
			Data: &waHistorySync.HistorySync{
				SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
				Conversations: []*waHistorySync.Conversation{{
					ID:                       proto.String(lastKnown.Chat.String()),
					EndOfHistoryTransfer:     proto.Bool(true),
					EndOfHistoryTransferType: waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum(),
					Messages:                 []*waHistorySync.HistorySyncMsg{{Message: older}},
				}},
			},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := a.FillHistory(ctx, BackfillFillOptions{
		LimitChats:      10,
		RequestsPerChat: 1,
		Count:           50,
		WaitPerRequest:  time.Second,
		IdleExit:        200 * time.Millisecond,
		ResetInProgress: true,
	})
	if err != nil {
		t.Fatalf("FillHistory: %v", err)
	}
	if res.Attempted != 1 {
		t.Fatalf("expected 1 attempted chat, got %d", res.Attempted)
	}
	if res.Blocked != 1 {
		t.Fatalf("expected 1 blocked chat, got %d", res.Blocked)
	}
	if res.Completed != 1 {
		t.Fatalf("expected 1 completed chat, got %d", res.Completed)
	}
	if res.MessagesAdded <= 0 {
		t.Fatalf("expected messages added, got %d", res.MessagesAdded)
	}

	readyState, err := a.db.GetBackfillState(chatReady.String())
	if err != nil {
		t.Fatalf("GetBackfillState ready: %v", err)
	}
	if readyState.Status != store.BackfillStatusComplete || !readyState.ReachedStart {
		t.Fatalf("expected ready chat complete, got %+v", readyState)
	}

	blockedState, err := a.db.GetBackfillState(chatBlocked.String())
	if err != nil {
		t.Fatalf("GetBackfillState blocked: %v", err)
	}
	if blockedState.Status != store.BackfillStatusBlocked || blockedState.BlockedReason != store.BackfillBlockedNoLocalAnchor {
		t.Fatalf("expected blocked chat no_local_anchor, got %+v", blockedState)
	}
}

func TestFillHistoryMarksStalledAfterRepeatedNoProgress(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	chat := types.JID{User: "999", Server: types.DefaultUserServer}
	base := time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC)

	if err := a.db.UpsertChat(chat.String(), "dm", "Stalled", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(chat.String(), "m2", base.Add(2*time.Second), "newer")); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}

	f.onDemandHistory = func(lastKnown types.MessageInfo, count int) *events.HistorySync {
		return &events.HistorySync{
			Data: &waHistorySync.HistorySync{
				SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
				Conversations: []*waHistorySync.Conversation{{
					ID:       proto.String(lastKnown.Chat.String()),
					Messages: nil,
				}},
			},
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := a.FillHistory(ctx, BackfillFillOptions{
		ChatJIDs:        []string{chat.String()},
		LimitChats:      10,
		RequestsPerChat: 2,
		Count:           50,
		WaitPerRequest:  time.Second,
		IdleExit:        200 * time.Millisecond,
		ResetInProgress: true,
	})
	if err != nil {
		t.Fatalf("FillHistory: %v", err)
	}
	if res.Attempted != 1 || res.Stalled != 1 {
		t.Fatalf("expected one stalled chat, got %+v", res)
	}

	state, err := a.db.GetBackfillState(chat.String())
	if err != nil {
		t.Fatalf("GetBackfillState: %v", err)
	}
	if state.Status != store.BackfillStatusStalled {
		t.Fatalf("expected stalled state, got %+v", state)
	}
	if state.ConsecutiveNoopRequests != 2 {
		t.Fatalf("expected 2 noop requests, got %+v", state)
	}
}

func TestPlanFillHistoryResumeOnlyUsesTrackedChats(t *testing.T) {
	a := newTestApp(t)

	base := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)
	chatFresh := "111@s.whatsapp.net"
	chatTracked := "222@s.whatsapp.net"

	if err := a.db.UpsertChat(chatFresh, "dm", "Fresh", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("UpsertChat fresh: %v", err)
	}
	if err := a.db.UpsertChat(chatTracked, "dm", "Tracked", base.Add(1*time.Minute)); err != nil {
		t.Fatalf("UpsertChat tracked: %v", err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(chatFresh, "m1", base.Add(1*time.Second), "fresh")); err != nil {
		t.Fatalf("UpsertMessage fresh: %v", err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(chatTracked, "m2", base.Add(2*time.Second), "tracked")); err != nil {
		t.Fatalf("UpsertMessage tracked: %v", err)
	}
	if err := a.db.PutBackfillState(store.BackfillState{
		ChatJID:   chatTracked,
		Status:    store.BackfillStatusReady,
		UpdatedAt: base.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("PutBackfillState: %v", err)
	}

	plan, err := a.PlanFillHistory(BackfillFillOptions{
		LimitChats: 10,
		ResumeOnly: true,
	})
	if err != nil {
		t.Fatalf("PlanFillHistory: %v", err)
	}
	if plan.Selected != 1 {
		t.Fatalf("expected 1 selected tracked chat, got %+v", plan)
	}
	if len(plan.Coverage) != 1 || plan.Coverage[0].ChatJID != chatTracked {
		t.Fatalf("expected only tracked coverage row, got %+v", plan.Coverage)
	}
}
