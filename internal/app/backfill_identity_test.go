package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBackfillHistoryResolvesChatIdentity(t *testing.T) {
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("100000000001", types.HiddenUserServer)
	group := types.NewJID("123", types.GroupServer)
	for _, tc := range []struct {
		name                       string
		input, stored, wire, reply types.JID
		mapped, manual             bool
	}{
		{"phone input and LID response", pn, pn, lid, lid, true, false},
		{"LID input and phone response", lid, pn, lid, pn, true, false},
		{"legacy LID storage", lid, lid, lid, lid, true, false},
		{"phone input and phone response", pn, pn, lid, pn, true, false},
		{"manual LID response", pn, pn, lid, lid, true, true},
		{"unmapped phone", pn, pn, pn, pn, false, false},
		{"unmapped LID", lid, lid, lid, lid, false, false},
		{"group", group, group, group, group, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			canonical := tc.stored
			if tc.mapped {
				f.lids[lid] = pn
				canonical = pn
			}
			base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := a.db.UpsertChat(tc.stored.String(), "dm", "Test contact", base); err != nil {
				t.Fatal(err)
			}
			m := storeUpsertMessage(tc.stored.String(), "latest", base, "existing")
			m.FromMe = true
			if err := a.db.UpsertMessage(m); err != nil {
				t.Fatal(err)
			}
			calls := 0
			response := func(info types.MessageInfo, count int) *events.HistorySync {
				calls++
				if info.Chat != tc.wire || count != 50 {
					t.Errorf("request chat = %s, count = %d; want %s, 50", info.Chat, count, tc.wire)
					return nil
				}
				wantID := "latest"
				if calls == 2 {
					wantID = "older-1"
				}
				wantTime := base.Add(-time.Duration(calls-1) * time.Second)
				if info.ID != wantID || !info.Timestamp.Equal(wantTime) || info.IsFromMe != (calls == 1) {
					t.Errorf("request anchor = %+v, want %s at %s", info, wantID, wantTime)
				}
				hs := backfillTestResponse(tc.reply.String(), fmt.Sprintf("older-%d", calls), base.Add(-time.Duration(calls)*time.Second))
				if calls == 2 {
					hs.Data.Conversations[0].EndOfHistoryTransferType = waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum()
				}
				return hs
			}
			if tc.manual {
				notif := &waE2E.HistorySyncNotification{SyncType: waE2E.HistorySyncType_ON_DEMAND.Enum()}
				var pending *events.HistorySync
				f.onDemandEvent = func(info types.MessageInfo, count int) any {
					pending = response(info, count)
					if pending == nil {
						return nil
					}
					return &events.Message{Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{HistorySyncNotification: notif}}}
				}
				f.downloadHistory = func(*waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
					return pending.Data, nil
				}
			} else {
				f.onDemandHistory = response
			}
			opts := backfillRetryOptions(tc.input.String())
			opts.Requests = 3
			res, err := a.BackfillHistory(context.Background(), opts)
			if err != nil {
				t.Fatalf("BackfillHistory: %v", err)
			}
			if res.ChatJID != canonical.String() || res.RequestsSent != 2 || res.ResponsesSeen != 2 || res.MessagesAdded != 2 {
				t.Fatalf("result = %+v, canonical chat = %s", res, canonical)
			}
			oldest, err := a.db.GetOldestMessageInfo(canonical.String())
			if err != nil || oldest.MsgID != "older-2" {
				t.Fatalf("oldest = %+v, err = %v", oldest, err)
			}
		})
	}
}

func TestBackfillHistoryIgnoresUnrelatedChatResponse(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("100000000001", types.HiddenUserServer)
	f.lids[lid] = pn
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(pn.String(), "dm", "Test contact", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(pn.String(), "anchor", base, "existing")); err != nil {
		t.Fatal(err)
	}
	f.onDemandHistory = func(types.MessageInfo, int) *events.HistorySync {
		return &events.HistorySync{Data: &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:                       proto.String("199999999999@lid"),
				EndOfHistoryTransferType: waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum(),
			}},
		}}
	}
	if _, err := a.BackfillHistory(context.Background(), backfillRetryOptions(pn.String())); err == nil {
		t.Fatal("unrelated chat response must not complete backfill")
	}
}

func TestBackfillHistoryResolvesMappingLearnedOnConnect(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("100000000001", types.HiddenUserServer)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(lid.String(), "dm", "Test contact", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertMessage(storeUpsertMessage(lid.String(), "anchor", base, "existing")); err != nil {
		t.Fatal(err)
	}
	// A freshly learned mapping causes Sync's post-connect migration to move
	// this anchor to the PN before Backfill's AfterConnect callback runs.
	f.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Connected); ok {
			f.mu.Lock()
			f.lids[lid] = pn
			f.mu.Unlock()
		}
	})
	f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		if info.Chat != lid || info.ID != "anchor" || !info.Timestamp.Equal(base) {
			t.Errorf("request = %+v; want LID with original local anchor", info)
		}
		return backfillTestResponse(lid.String(), "older", base.Add(-time.Second))
	}
	res, err := a.BackfillHistory(context.Background(), backfillRetryOptions(lid.String()))
	if err != nil {
		t.Fatalf("BackfillHistory after mapping learned: %v", err)
	}
	if res.ChatJID != pn.String() || res.RequestsSent != 1 || res.ResponsesSeen != 1 || res.MessagesAdded != 1 {
		t.Fatalf("result = %+v; want canonical PN with one completed request", res)
	}
	oldest, err := a.db.GetOldestMessageInfo(pn.String())
	if err != nil || oldest.MsgID != "older" {
		t.Fatalf("canonical oldest = %+v, err = %v", oldest, err)
	}
}
