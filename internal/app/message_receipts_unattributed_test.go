package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// whatsmeow names a grouped group receipt's message from the participants
// node's key or message_id attribute and leaves the ID blank when WhatsApp
// sends neither. Such entries cannot be matched to a message, and WhatsApp
// does not resend them, so they must not hold back follow-mode freshness.
func TestFollowSyncRecordsFreshnessDespiteUnattributedGroupReceipts(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	group := types.NewJID("999", types.GroupServer)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(group.String(), "group", "synthetic", at); err != nil {
		t.Fatal(err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: group.String(), MsgID: "valid", Timestamp: at, FromMe: true, Text: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	a.opts.Events = out.NewEventWriter(&log, true)

	groupReceipt := func(participant string, ids ...types.MessageID) *events.Receipt {
		return &events.Receipt{
			MessageSource: types.MessageSource{Chat: group, Sender: types.NewJID(participant, types.DefaultUserServer), IsGroup: true},
			MessageIDs:    ids,
			Type:          types.ReceiptTypeRead,
			Timestamp:     at,
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _ = a.Sync(ctx, SyncOptions{Mode: SyncModeFollow, AfterConnect: func(context.Context) error {
		for _, participant := range []string{"111", "222", "333"} {
			f.emit(groupReceipt(participant, ""))
		}
		f.emit(groupReceipt("444", "valid"))
		f.emit(&events.OfflineSyncCompleted{Count: 4})

		marker, err := a.db.LastSuccessfulSync()
		if err != nil || marker == nil {
			t.Errorf("freshness marker=%v err=%v; want it recorded", marker, err)
		}
		counts, err := a.db.MessageReceipts(group.String(), "valid")
		if err != nil || counts.Read != 1 {
			t.Errorf("valid receipt counts=%+v err=%v; want read=1", counts, err)
		}
		cancel()
		return nil
	}})

	if n := strings.Count(log.String(), "message_receipt_unattributed"); n != 1 {
		t.Errorf("unattributed warnings=%d; want one per sync: %s", n, &log)
	}
	if strings.Contains(log.String(), "message_receipt_invalid") {
		t.Errorf("unattributed receipts reported as invalid input: %s", &log)
	}
}
