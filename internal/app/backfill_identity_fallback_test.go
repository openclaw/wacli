package app

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Regressions for #444: the primary device answers some mapped 1:1 chats only
// by phone number and others only by LID.

type identityFallbackTest struct {
	a      *App
	f      *fakeWA
	pn     types.JID
	lid    types.JID
	base   time.Time
	events *bytes.Buffer
}

func newIdentityFallbackTest(t *testing.T, ids ...string) identityFallbackTest {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var eventLog bytes.Buffer
	a.opts.Events = out.NewEventWriter(&eventLog, true)
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("100000000001", types.HiddenUserServer)
	f.lids[lid] = pn
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(pn.String(), "dm", "Test contact", base); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if err := a.db.UpsertMessage(storeUpsertMessage(pn.String(), id, base.Add(time.Duration(i)*time.Second), "existing")); err != nil {
			t.Fatal(err)
		}
	}
	return identityFallbackTest{a: a, f: f, pn: pn, lid: lid, base: base, events: &eventLog}
}

func (tt identityFallbackTest) identityRetryWarnings(t *testing.T) []map[string]any {
	t.Helper()
	var warnings []map[string]any
	for line := range bytes.SplitSeq(bytes.TrimSpace(tt.events.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("invalid lifecycle JSON: %s: %v", line, err)
		}
		if event.Data["code"] == "backfill_identity_retry" {
			warnings = append(warnings, event.Data)
		}
	}
	return warnings
}

func requestLabel(info types.MessageInfo) string {
	return info.Chat.Server + ":" + info.ID
}

func TestBackfillHistoryRetriesPhoneIdentityWhenLIDIsSilent(t *testing.T) {
	tt := newIdentityFallbackTest(t, "m0")
	var requests []string
	pnCalls := 0
	tt.f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		requests = append(requests, requestLabel(info))
		if info.Chat != tt.pn {
			return nil // this chat is filed under the phone number
		}
		pnCalls++
		hs := backfillTestResponse(tt.pn.String(), "older-"+string(rune('0'+pnCalls)), tt.base.Add(-time.Duration(pnCalls)*time.Second))
		if pnCalls == 2 {
			hs.Data.Conversations[0].EndOfHistoryTransferType = waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum()
		}
		return hs
	}
	opts := backfillRetryOptions(tt.pn.String())
	opts.Requests = 3
	res, err := tt.a.BackfillHistory(context.Background(), opts)
	if err != nil {
		t.Fatalf("BackfillHistory: %v (requests %v)", err, requests)
	}
	// The second batch goes straight to the identity that answered.
	want := []string{"lid:m0", "s.whatsapp.net:m0", "s.whatsapp.net:older-1"}
	if !slices.Equal(requests, want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	if res.RequestsSent != 3 || res.ResponsesSeen != 2 || res.MessagesAdded != 2 {
		t.Fatalf("result = %+v", res)
	}
	warnings := tt.identityRetryWarnings(t)
	if len(warnings) != 1 || warnings[0]["request_chat_jid"] != tt.lid.String() || warnings[0]["retry_request_chat_jid"] != tt.pn.String() {
		t.Fatalf("identity retry warnings = %v", warnings)
	}
}

func TestBackfillHistoryKeepsLIDWhenItAnswers(t *testing.T) {
	tt := newIdentityFallbackTest(t, "m0")
	var requests []string
	tt.f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		requests = append(requests, requestLabel(info))
		if info.Chat != tt.lid {
			t.Errorf("unexpected phone-number request %+v", info)
			return nil
		}
		hs := backfillTestResponse(tt.lid.String(), "older", tt.base.Add(-time.Second))
		hs.Data.Conversations[0].EndOfHistoryTransferType = waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum()
		return hs
	}
	res, err := tt.a.BackfillHistory(context.Background(), backfillRetryOptions(tt.pn.String()))
	if err != nil {
		t.Fatalf("BackfillHistory: %v", err)
	}
	if !slices.Equal(requests, []string{"lid:m0"}) || res.RequestsSent != 1 || res.MessagesAdded != 1 {
		t.Fatalf("requests = %v, result = %+v", requests, res)
	}
	if warnings := tt.identityRetryWarnings(t); len(warnings) != 0 {
		t.Fatalf("unexpected identity retry: %v", warnings)
	}
}

func TestBackfillHistoryTriesBothIdentitiesForEachAnchor(t *testing.T) {
	tt := newIdentityFallbackTest(t, "m0", "m1")
	var requests []string
	tt.f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		requests = append(requests, requestLabel(info))
		return nil
	}
	_, err := tt.a.BackfillHistory(context.Background(), backfillRetryOptions(tt.pn.String()))
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for on-demand history sync response") {
		t.Fatalf("error = %v, want timeout", err)
	}
	want := []string{"lid:m0", "s.whatsapp.net:m0", "lid:m1", "s.whatsapp.net:m1"}
	if !slices.Equal(requests, want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	if warnings := tt.identityRetryWarnings(t); len(warnings) != 2 {
		t.Fatalf("identity retry warnings = %d, want 2", len(warnings))
	}
}

func TestBackfillHistoryUnmappedChatHasNoIdentityRetry(t *testing.T) {
	tt := newIdentityFallbackTest(t, "m0")
	delete(tt.f.lids, tt.lid)
	var requests []string
	tt.f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		requests = append(requests, requestLabel(info))
		return nil
	}
	if _, err := tt.a.BackfillHistory(context.Background(), backfillRetryOptions(tt.pn.String())); err == nil {
		t.Fatal("expected timeout")
	}
	if !slices.Equal(requests, []string{"s.whatsapp.net:m0"}) {
		t.Fatalf("requests = %v", requests)
	}
}

func TestBackfillHistoryRecoversPreferenceAfterLateFirstResponse(t *testing.T) {
	tt := newIdentityFallbackTest(t, "m0")
	var requests []string
	tt.f.onDemandHistory = func(info types.MessageInfo, _ int) *events.HistorySync {
		requests = append(requests, requestLabel(info))
		switch len(requests) {
		case 2:
			// The first LID response arrives during the PN attempt. HistorySync
			// identifies the chat, not the request that elicited it.
			return backfillTestResponse(tt.lid.String(), "older-1", tt.base.Add(-time.Second))
		case 4:
			return backfillTestResponse(tt.lid.String(), "older-2", tt.base.Add(-2*time.Second))
		case 5:
			hs := backfillTestResponse(tt.lid.String(), "older-3", tt.base.Add(-3*time.Second))
			hs.Data.Conversations[0].EndOfHistoryTransferType = waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY.Enum()
			return hs
		default:
			return nil
		}
	}
	opts := backfillRetryOptions(tt.pn.String())
	opts.Requests = 3
	res, err := tt.a.BackfillHistory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lid:m0", "s.whatsapp.net:m0", "s.whatsapp.net:older-1", "lid:older-1", "lid:older-2"}
	if !slices.Equal(requests, want) || res.MessagesAdded != 3 {
		t.Fatalf("requests=%v result=%+v", requests, res)
	}
}
