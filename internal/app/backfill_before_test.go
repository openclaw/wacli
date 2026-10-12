package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// A gap between two stored messages cannot be reached from the oldest anchor;
// --before anchors the request on the newer side of the gap instead.
func TestBackfillHistoryBeforeFillsGap(t *testing.T) {
	a, f, chat, base := newBackfillRetryTest(t, "m0", "m1")
	var anchors []string
	f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		anchors = append(anchors, string(info.ID))
		return backfillTestResponse(chat, "gap", base.Add(time.Second))
	}
	opts := backfillRetryOptions(chat)
	opts.Before = "m1"
	res, err := a.BackfillHistory(context.Background(), opts)
	if err != nil {
		t.Fatalf("BackfillHistory: %v", err)
	}
	if !slices.Equal(anchors, []string{"m1"}) {
		t.Fatalf("anchors = %v, want [m1]", anchors)
	}
	if res.RequestsSent != 1 || res.ResponsesSeen != 1 || res.MessagesAdded != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := a.db.GetMessage(chat, "gap"); err != nil {
		t.Fatalf("gap message not stored: %v", err)
	}
}

func TestBackfillHistoryBeforeRejectsUnknownMessage(t *testing.T) {
	a, f, chat, _ := newBackfillRetryTest(t, "m0")
	f.onDemandHistory = func(info types.MessageInfo, count int) *events.HistorySync {
		t.Errorf("unexpected request %+v", info)
		return nil
	}
	opts := backfillRetryOptions(chat)
	opts.Before = "missing"
	_, err := a.BackfillHistory(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "message missing not found") {
		t.Fatalf("error = %v, want unknown anchor error", err)
	}
}
