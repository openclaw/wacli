package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestBackfillHistoryRetriesUnansweredAnchor(t *testing.T) {
	for _, firstID := range []string{"3EB0-stalled", "AC-stalled"} {
		t.Run(firstID, func(t *testing.T) {
			a, f, chat, base := newBackfillRetryTest(t, firstID, "fallback")
			var eventLog bytes.Buffer
			a.opts.Events = out.NewEventWriter(&eventLog, true)
			var anchors []string
			f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
				anchors = append(anchors, info.ID)
				if info.ID == firstID {
					return nil
				}
				if info.Chat.String() != chat || !info.IsFromMe || !info.Timestamp.Equal(base) || count != 50 {
					t.Errorf("fallback request = %+v, count = %d", info, count)
				}
				return backfillTestResponse(chat, "older", base.Add(-time.Second))
			}
			res, err := a.BackfillHistory(context.Background(), backfillRetryOptions(chat))
			if err != nil {
				t.Fatalf("BackfillHistory: %v (anchors %v)", err, anchors)
			}
			if !slices.Equal(anchors, []string{firstID, "fallback"}) {
				t.Fatalf("anchors = %v", anchors)
			}
			if res.RequestsSent != 2 || res.ResponsesSeen != 1 || res.MessagesAdded != 1 {
				t.Fatalf("result = %+v", res)
			}
			oldest, err := a.db.GetOldestMessageInfo(chat)
			if err != nil || oldest.MsgID != "older" {
				t.Fatalf("oldest = %+v, err = %v", oldest, err)
			}
			warnings := 0
			for _, line := range bytes.Split(bytes.TrimSpace(eventLog.Bytes()), []byte("\n")) {
				var event struct {
					Data map[string]any `json:"data"`
				}
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatalf("invalid lifecycle JSON: %s: %v", line, err)
				}
				if event.Data["code"] == "backfill_anchor_retry" {
					warnings++
					if event.Data["anchor_msg_id"] != firstID || event.Data["retry_anchor_msg_id"] != "fallback" {
						t.Fatalf("retry warning = %v", event)
					}
				}
			}
			if warnings != 1 {
				t.Fatalf("retry warnings = %d, want 1; events: %s", warnings, &eventLog)
			}
		})
	}
}

func TestBackfillHistoryRetryStops(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ids        []string
		cancel     bool
		noProgress bool
		wantCalls  int
	}{
		{name: "single anchor", ids: []string{"first"}, wantCalls: 1},
		{name: "two silent anchors", ids: []string{"first", "second", "third"}, wantCalls: 2},
		{name: "cancelled", ids: []string{"first", "second"}, cancel: true, wantCalls: 1},
		{name: "fallback without older history", ids: []string{"first", "second", "third"}, noProgress: true, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, chat, base := newBackfillRetryTest(t, tc.ids...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
				calls++
				if tc.cancel {
					cancel()
				}
				if tc.noProgress && calls == 2 {
					return backfillTestResponse(chat, "first", base)
				}
				return nil
			}
			opts := backfillRetryOptions(chat)
			opts.Requests = 3
			_, err := a.BackfillHistory(ctx, opts)
			switch {
			case tc.cancel:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want cancellation", err)
				}
			case tc.noProgress:
				if err != nil {
					t.Fatalf("BackfillHistory: %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "timed out waiting for on-demand history sync response") {
					t.Fatalf("error = %v, want timeout", err)
				}
			}
			if calls != tc.wantCalls {
				t.Fatalf("requests = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestBackfillHistoryContinuesFromRecoveredOldest(t *testing.T) {
	a, f, chat, base := newBackfillRetryTest(t, "stalled", "fallback")
	var anchors []string
	f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		anchors = append(anchors, info.ID)
		switch info.ID {
		case "fallback":
			return backfillTestResponse(chat, "older", base.Add(-time.Second))
		case "older":
			return backfillTestResponse(chat, "oldest", base.Add(-2*time.Second))
		default:
			return nil
		}
	}
	opts := backfillRetryOptions(chat)
	opts.Requests = 2
	res, err := a.BackfillHistory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(anchors, []string{"stalled", "fallback", "older"}) || res.RequestsSent != 3 || res.ResponsesSeen != 2 || res.MessagesAdded != 2 {
		t.Fatalf("anchors = %v, result = %+v", anchors, res)
	}
}

func TestBackfillHistoryDoesNotRetryTransportError(t *testing.T) {
	a, f, chat, _ := newBackfillRetryTest(t, "first", "second")
	f.onDemandErr = errors.New("transport unavailable")
	var eventLog bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventLog, true)
	_, err := a.BackfillHistory(context.Background(), backfillRetryOptions(chat))
	if !errors.Is(err, f.onDemandErr) {
		t.Fatalf("error = %v, want transport error", err)
	}
	if strings.Count(eventLog.String(), `"event":"backfill_requesting"`) != 1 || strings.Contains(eventLog.String(), "backfill_anchor_retry") {
		t.Fatalf("unexpected retry: %s", &eventLog)
	}
}

func newBackfillRetryTest(t *testing.T, ids ...string) (*App, *fakeWA, string, time.Time) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := "123@g.us"
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat, "group", "Test group", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for i, id := range ids {
		// Equal timestamps exercise the stable row-ID ordering of adjacent anchors.
		msg := storeUpsertMessage(chat, id, base, "existing text")
		msg.FromMe = i == 1
		if err := a.db.UpsertMessage(msg); err != nil {
			t.Fatalf("UpsertMessage: %v", err)
		}
	}
	return a, f, chat, base
}

func backfillRetryOptions(chat string) BackfillOptions {
	return BackfillOptions{ChatJID: chat, Count: 50, Requests: 1, WaitPerRequest: 10 * time.Millisecond, IdleExit: time.Millisecond}
}

func backfillTestResponse(chat, id string, timestamp time.Time) *events.HistorySync {
	return &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
		Conversations: []*waHistorySync.Conversation{{
			ID: proto.String(chat),
			Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
				Key:              &waCommon.MessageKey{RemoteJID: proto.String(chat), FromMe: proto.Bool(false), ID: proto.String(id)},
				MessageTimestamp: proto.Uint64(uint64(timestamp.Unix())),
				Message:          &waProto.Message{Conversation: proto.String("older text")},
			}}},
		}},
	}}
}

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
	if got := f.manualHistorySyncCalls; len(got) != 4 || !got[0] || !got[1] || got[2] || got[3] {
		t.Fatalf("manual history sync calls = %v, want [true true false false]", got)
	}
}

func TestBackfillHistoryDownloadsManualOnDemandNotification(t *testing.T) {
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

	syncType := waE2E.HistorySyncType_ON_DEMAND
	notif := &waE2E.HistorySyncNotification{SyncType: &syncType}
	f.onDemandEvent = func(lastKnown types.MessageInfo, count int) interface{} {
		return &events.Message{
			Message: &waProto.Message{
				ProtocolMessage: &waProto.ProtocolMessage{
					HistorySyncNotification: notif,
				},
			},
		}
	}
	downloadCalls := 0
	f.downloadHistory = func(got *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error) {
		downloadCalls++
		if got != notif {
			t.Fatalf("DownloadHistorySync notification = %p, want %p", got, notif)
		}
		older := &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(chatStr),
				FromMe:    proto.Bool(false),
				ID:        proto.String("m1"),
			},
			MessageTimestamp: proto.Uint64(uint64(base.Add(1 * time.Second).Unix())),
			Message:          &waProto.Message{Conversation: proto.String("older")},
		}
		return &waHistorySync.HistorySync{
			SyncType: waHistorySync.HistorySync_ON_DEMAND.Enum(),
			Conversations: []*waHistorySync.Conversation{{
				ID:                       proto.String(chatStr),
				EndOfHistoryTransferType: waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum(),
				Messages:                 []*waHistorySync.HistorySyncMsg{{Message: older}},
			}},
		}, nil
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
	if downloadCalls != 1 {
		t.Fatalf("download calls = %d, want 1", downloadCalls)
	}
	if res.MessagesAdded <= 0 {
		t.Fatalf("expected messages to be added, got %d", res.MessagesAdded)
	}
}

func TestNormalizeBackfillOptions(t *testing.T) {
	opts := normalizeBackfillOptions(BackfillOptions{})

	if opts.Count != DefaultBackfillCount {
		t.Fatalf("Count = %d, want %d", opts.Count, DefaultBackfillCount)
	}
	if opts.Requests != DefaultBackfillRequests {
		t.Fatalf("Requests = %d, want %d", opts.Requests, DefaultBackfillRequests)
	}
	if opts.WaitPerRequest <= 0 || opts.IdleExit <= 0 {
		t.Fatalf("durations must default positive: %+v", opts)
	}
}

func TestValidateBackfillOptionsCapsWork(t *testing.T) {
	err := validateBackfillOptions(BackfillOptions{
		Count:    MaxBackfillCount + 1,
		Requests: DefaultBackfillRequests,
	})
	if err == nil || !strings.Contains(err.Error(), "--count") {
		t.Fatalf("count error = %v", err)
	}

	err = validateBackfillOptions(BackfillOptions{
		Count:    DefaultBackfillCount,
		Requests: MaxBackfillRequests + 1,
	})
	if err == nil || !strings.Contains(err.Error(), "--requests") {
		t.Fatalf("requests error = %v", err)
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
