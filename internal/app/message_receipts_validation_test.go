package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestOutgoingReceiptValidation(t *testing.T) {
	pn := types.NewJID("123", types.DefaultUserServer)
	lid := types.NewJID("456", types.HiddenUserServer)
	group := types.NewJID("999", types.GroupServer)
	for _, tc := range []struct {
		name                  string
		ids                   []types.MessageID
		sender, chat          types.JID
		invalid, closeDB      bool
		wantValid, wantBefore int
	}{
		{name: "blank", ids: []types.MessageID{""}, invalid: true},
		{name: "whitespace", ids: []types.MessageID{" \t\n"}, invalid: true},
		{name: "invalid_before_valid", ids: []types.MessageID{"", "valid"}, invalid: true, wantValid: 1},
		{name: "valid_before_invalid", ids: []types.MessageID{"valid", " "}, invalid: true, wantValid: 1},
		{name: "valid_both_sides", ids: []types.MessageID{"before", "", "valid"}, invalid: true, wantValid: 1, wantBefore: 1},
		{name: "valid_persistence", ids: []types.MessageID{"valid"}, sender: pn, wantValid: 1},
		{name: "empty_sender_fallback", ids: []types.MessageID{"valid"}, wantValid: 1},
		{name: "malformed_empty_user", ids: []types.MessageID{"valid"}, sender: types.JID{Server: types.DefaultUserServer}, invalid: true},
		{name: "malformed_whitespace_server", ids: []types.MessageID{"valid"}, sender: types.JID{Server: " "}, invalid: true},
		{name: "closed_store", ids: []types.MessageID{"valid"}, closeDB: true},
		{name: "sender_missing_server", ids: []types.MessageID{"valid"}, sender: types.JID{User: "123"}, invalid: true},
		{name: "sender_whitespace_user", ids: []types.MessageID{"valid"}, sender: types.JID{User: " \t", Server: types.DefaultUserServer}, invalid: true},
		{name: "sender_embedded_space", ids: []types.MessageID{"valid"}, sender: types.JID{User: "12 3", Server: types.DefaultUserServer}, invalid: true},
		{name: "sender_embedded_at", ids: []types.MessageID{"valid"}, sender: types.JID{User: "12@3", Server: types.DefaultUserServer}, invalid: true},
		{name: "malformed_chat", ids: []types.MessageID{"valid"}, chat: types.JID{Server: types.DefaultUserServer}, invalid: true},
		{name: "group_participant", ids: []types.MessageID{"valid"}, chat: group, sender: pn, wantValid: 1},
		{name: "group_missing_participant", ids: []types.MessageID{"valid"}, chat: group, invalid: true},
		{name: "linked_identity", ids: []types.MessageID{"valid"}, sender: lid, wantValid: 1},
		{name: "phone_device", ids: []types.MessageID{"valid"}, sender: types.JID{User: "123", Server: types.DefaultUserServer, Device: 2}, wantValid: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			f.connected = true
			f.lids[lid] = pn
			a.wa = f
			chat := pn
			if tc.chat != (types.JID{}) {
				chat = tc.chat
			}
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := a.db.UpsertChat(chat.String(), "dm", "synthetic", at); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"before", "valid"} {
				if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, Timestamp: at, FromMe: true, Text: "synthetic"}); err != nil {
					t.Fatal(err)
				}
			}
			var log bytes.Buffer
			a.opts.Events = out.NewEventWriter(&log, true)
			var stored, last atomic.Int64
			limits := &syncStorageLimits{app: a}
			a.addSyncEventHandler(t.Context(), SyncOptions{}, &stored, &last, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1), nil, nil, limits, &syncPresence{}, nil)
			if tc.closeDB {
				if err := a.db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, Sender: tc.sender, IsGroup: chat.Server == types.GroupServer}, MessageIDs: tc.ids, Type: types.ReceiptTypeRead, Timestamp: at}
			wantFailure := tc.invalid || tc.closeDB
			if err := a.handleOutgoingReceiptEvent(t.Context(), receipt); (err != nil) != wantFailure {
				t.Errorf("returned error=%v; want failure=%v", err, wantFailure)
			}
			f.emit(receipt)
			if limits.persistenceFailed.Load() != wantFailure || limits.canRecordFreshness() == wantFailure {
				t.Errorf("failure=%v freshness=%v", limits.persistenceFailed.Load(), limits.canRecordFreshness())
			}
			if strings.Contains(log.String(), "message_receipt_invalid") != tc.invalid {
				t.Errorf("input warning mismatch: %s", &log)
			}
			if strings.Contains(log.String(), "message_receipt_store_failed") != tc.closeDB {
				t.Errorf("store warning mismatch: %s", &log)
			}
			if tc.closeDB {
				if !strings.Contains(log.String(), "database is closed") {
					t.Errorf("not a real store error: %s", &log)
				}
				return
			}
			for id, want := range map[string]int{"valid": tc.wantValid, "before": tc.wantBefore} {
				counts, err := a.db.MessageReceipts(chat.String(), id)
				if err != nil || counts.Read != want {
					t.Errorf("%s counts=%+v err=%v; want read=%d", id, counts, err, want)
				}
			}
			f.emit(&events.OfflineSyncCompleted{Count: 1})
			marker, err := a.db.LastSuccessfulSync()
			if err != nil || (marker == nil) != wantFailure {
				t.Errorf("offline marker=%v err=%v; want suppressed=%v", marker, err, wantFailure)
			}
			if tc.name == "linked_identity" || tc.name == "phone_device" {
				raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				var recipient string
				if err := raw.QueryRow(`SELECT recipient_jid FROM message_receipts WHERE msg_id='valid'`).Scan(&recipient); err != nil || recipient != pn.String() {
					t.Errorf("canonical recipient=%q err=%v", recipient, err)
				}
			}
		})
	}
}

// Exercise both actual marker-write paths, not only canRecordFreshness.
func TestOutgoingReceiptSyncMarkers(t *testing.T) {
	for _, tc := range []struct {
		name              string
		ids               []types.MessageID
		sender            types.JID
		sqlFault, invalid bool
	}{
		{name: "blank", ids: []types.MessageID{""}, invalid: true},
		{name: "mixed", ids: []types.MessageID{"before", "", "valid"}, invalid: true},
		{name: "malformed_recipient", ids: []types.MessageID{"valid"}, sender: types.JID{Server: types.DefaultUserServer}, invalid: true},
		{name: "sql_failure", ids: []types.MessageID{"valid"}, sqlFault: true},
		{name: "invalid_and_sql_failure", ids: []types.MessageID{"", "valid"}, invalid: true, sqlFault: true},
		{name: "valid_control", ids: []types.MessageID{"valid"}},
	} {
		for _, offline := range []bool{false, true} {
			name := tc.name + "/idle"
			if offline {
				name = tc.name + "/offline"
			}
			t.Run(name, func(t *testing.T) {
				a := newTestApp(t)
				f := newFakeWA()
				a.wa = f
				pn := types.NewJID("123", types.DefaultUserServer)
				at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				if err := a.db.UpsertChat(pn.String(), "dm", "synthetic", at); err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"before", "valid"} {
					if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: pn.String(), MsgID: id, Timestamp: at, FromMe: true, Text: "synthetic"}); err != nil {
						t.Fatal(err)
					}
				}
				if tc.sqlFault {
					raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer raw.Close()
					if _, err := raw.Exec(`CREATE TRIGGER refuse_receipt BEFORE INSERT ON message_receipts BEGIN SELECT RAISE(ABORT,'synthetic receipt write failure'); END`); err != nil {
						t.Fatal(err)
					}
				}
				var log bytes.Buffer
				a.opts.Events = out.NewEventWriter(&log, true)
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				_, err := a.Sync(ctx, SyncOptions{Mode: SyncModeOnce, IdleExit: time.Millisecond, AfterConnect: func(context.Context) error {
					f.emit(&events.Receipt{MessageSource: types.MessageSource{Chat: pn, Sender: tc.sender}, MessageIDs: tc.ids, Type: types.ReceiptTypeRead, Timestamp: at})
					if offline {
						f.emit(&events.OfflineSyncCompleted{Count: 1})
						marker, err := a.db.LastSuccessfulSync()
						if err != nil || (marker == nil) != (tc.invalid || tc.sqlFault) {
							t.Errorf("offline marker=%v err=%v", marker, err)
						}
					}
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				marker, err := a.db.LastSuccessfulSync()
				if err != nil || (marker == nil) != (tc.invalid || tc.sqlFault) {
					t.Errorf("final marker=%v err=%v", marker, err)
				}
				if strings.Contains(log.String(), "message_receipt_invalid") != tc.invalid || strings.Contains(log.String(), "message_receipt_store_failed") != tc.sqlFault {
					t.Errorf("warning classification: %s", &log)
				}
				if tc.sqlFault && !strings.Contains(log.String(), "synthetic receipt write failure") {
					t.Errorf("SQL error lost: %s", &log)
				}
			})
		}
	}
}

// Exercise persisted receipt counts and freshness through the shipped CLI.
func TestOutgoingReceiptMixedBatchCLI(t *testing.T) {
	dir := t.TempDir()
	if baseline := os.Getenv("WACLI_RECEIPT_BASELINE_BINARY"); baseline != "" {
		cmd := exec.Command(baseline, "--store", dir, "--json", "messages", "list")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("initialize baseline store: %v: %s", err, output)
		}
		t.Log("opened store created by baseline CLI")
	}
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	chat := types.NewJID("123", types.DefaultUserServer)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic receipt proof", at); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"before", "after"} {
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, Timestamp: at, FromMe: true, Text: "synthetic receipt proof"}); err != nil {
			t.Fatal(err)
		}
	}
	h := newRetrievalCLI(t, a)
	var warnings bytes.Buffer
	a.opts.Events = out.NewEventWriter(&warnings, true)
	receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeRead, MessageIDs: []types.MessageID{"before", "", "after"}, Timestamp: at.Add(time.Minute)}
	retrievalSyncEvents(t, a, receipt, &events.OfflineSyncCompleted{Count: 1})
	messages := h.run(dir, "", "messages", "list")
	if len(messages["messages"].([]any)) != 2 {
		t.Fatalf("expected two stored messages: %v", messages)
	}
	for _, row := range messages["messages"].([]any) {
		message := row.(map[string]any)
		if message["ReadBy"] != float64(1) || message["DeliveredTo"] != float64(1) {
			t.Fatalf("valid receipt lost: %v", message)
		}
	}
	if messages["source"].(map[string]any)["last_sync_at"] != nil {
		t.Fatal("mixed batch advanced successful-sync freshness")
	}
	if !strings.Contains(warnings.String(), "message_receipt_invalid") {
		t.Fatalf("missing input warning: %s", &warnings)
	}
	fmt.Fprintf(&h.log, "mixed batch warnings: %s\n", &warnings)
	receipt.MessageIDs = []types.MessageID{"before", "after"}
	retrievalSyncEvents(t, a, receipt, &events.OfflineSyncCompleted{Count: 1})
	if messages = h.run(dir, "", "messages", "list"); messages["source"].(map[string]any)["last_sync_at"] == nil {
		t.Fatal("valid follow-up did not restore successful-sync freshness")
	}
}
