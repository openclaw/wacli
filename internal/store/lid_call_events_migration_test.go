package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

type callRow struct {
	ChatJID   string
	SenderJID string
	CallID    string
	Outcome   string
	Duration  int64
}

func callRowsOf(t *testing.T, db *DB, where string, args ...any) []callRow {
	t.Helper()
	rows, err := db.sql.Query(`
		SELECT chat_jid, COALESCE(sender_jid, ''), call_id, COALESCE(outcome, ''), duration_secs
		FROM call_events WHERE `+where+` ORDER BY chat_jid, call_id, ts`, args...)
	if err != nil {
		t.Fatalf("query call_events: %v", err)
	}
	defer rows.Close()
	var out []callRow
	for rows.Next() {
		var r callRow
		if err := rows.Scan(&r.ChatJID, &r.SenderJID, &r.CallID, &r.Outcome, &r.Duration); err != nil {
			t.Fatalf("scan call_events: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows call_events: %v", err)
	}
	return out
}

// Calls recorded while only the hidden-user identity was known must survive the
// migration: call_events cascades on its chat row, so without a call step
// deleteLIDChat deletes them with no trace.
func TestMigrateLIDToPNMovesCallEvents(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	group := "120363000000@g.us"
	for _, jid := range []string{pn, lid, group} {
		if err := db.UpsertChat(jid, "dm", jid, base); err != nil {
			t.Fatalf("UpsertChat %s: %v", jid, err)
		}
	}
	upsert := func(p UpsertCallEventParams) {
		t.Helper()
		if err := db.UpsertCallEvent(p); err != nil {
			t.Fatalf("UpsertCallEvent %s/%s: %v", p.ChatJID, p.CallID, err)
		}
	}
	// two calls only the hidden-user chat holds
	upsert(UpsertCallEventParams{ChatJID: lid, SenderJID: lid, CallID: "lid-only-1", EventType: "offer", Direction: "incoming", Outcome: "missed", Timestamp: base})
	upsert(UpsertCallEventParams{ChatJID: lid, SenderJID: lid, CallID: "lid-only-2", EventType: "offer", Direction: "incoming", Outcome: "answered", DurationSecs: 30, Timestamp: base.Add(time.Hour)})
	// the same call recorded under both identities: the phone-number row keeps
	// its outcome and takes the duration it lacks
	upsert(UpsertCallEventParams{ChatJID: lid, SenderJID: lid, CallID: "both", EventType: "offer", Direction: "incoming", Outcome: "rejected", DurationSecs: 42, Timestamp: base.Add(2 * time.Hour)})
	upsert(UpsertCallEventParams{ChatJID: pn, SenderJID: pn, CallID: "both", EventType: "offer", Direction: "incoming", Outcome: "answered", Timestamp: base.Add(2 * time.Hour)})
	// a call only the phone-number chat holds, untouched
	upsert(UpsertCallEventParams{ChatJID: pn, SenderJID: pn, CallID: "pn-only", EventType: "offer", Direction: "incoming", Outcome: "missed", Timestamp: base.Add(3 * time.Hour)})
	// a group call the hidden user placed: the row stays in the group, its sender is rewritten
	upsert(UpsertCallEventParams{ChatJID: group, SenderJID: lid, CallID: "group-call", EventType: "offer", Direction: "incoming", Outcome: "missed", Timestamp: base.Add(4 * time.Hour)})

	got, err := db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs: %v", err)
	}
	if want := []string{lid}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HistoricalLIDJIDs = %#v, want %#v", got, want)
	}

	want := []callRow{
		{ChatJID: group, SenderJID: pn, CallID: "group-call", Outcome: "missed"},
		{ChatJID: pn, SenderJID: pn, CallID: "both", Outcome: "answered", Duration: 42},
		{ChatJID: pn, SenderJID: pn, CallID: "lid-only-1", Outcome: "missed"},
		{ChatJID: pn, SenderJID: pn, CallID: "lid-only-2", Outcome: "answered", Duration: 30},
		{ChatJID: pn, SenderJID: pn, CallID: "pn-only", Outcome: "missed"},
	}
	for pass := 1; pass <= 2; pass++ { // the migration runs at every sync start, so a second pass must change nothing
		if err := db.MigrateLIDToPN(lid, pn); err != nil {
			t.Fatalf("MigrateLIDToPN pass %d: %v", pass, err)
		}
		if got := callRowsOf(t, db, `1 = 1`); !reflect.DeepEqual(got, want) {
			t.Fatalf("pass %d call_events = %#v, want %#v", pass, got, want)
		}
	}
	if got := callRowsOf(t, db, `chat_jid = ? OR sender_jid = ?`, lid, lid); len(got) != 0 {
		t.Fatalf("hidden-user call rows left: %#v", got)
	}
}

// A hidden user seen only as the sender of a group call is still discovered,
// so its identity is migrated like any other.
func TestHistoricalLIDJIDsFindsCallSenderOnlyIdentity(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	group := "120363000000@g.us"
	lid := "999123456789@lid"
	if err := db.UpsertChat(group, "group", "group", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertCallEvent(UpsertCallEventParams{ChatJID: group, SenderJID: lid, CallID: "group-call", EventType: "offer", Direction: "incoming", Timestamp: base}); err != nil {
		t.Fatalf("UpsertCallEvent: %v", err)
	}
	got, err := db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs: %v", err)
	}
	if want := []string{lid}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HistoricalLIDJIDs = %#v, want %#v", got, want)
	}
}

func TestMigrateLIDCallEventsFailureRollsBack(t *testing.T) {
	db := openTestDB(t)
	lid, pn := "999123456789@lid", "15551234567@s.whatsapp.net"
	at := time.Unix(1700000000, 0)
	if err := db.UpsertChat(lid, "dm", "Synthetic", at); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertCallEvent(UpsertCallEventParams{ChatJID: lid, SenderJID: lid, CallID: "kept-on-failure", EventType: "offer", Timestamp: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`CREATE TRIGGER reject_call_move BEFORE INSERT ON call_events WHEN NEW.chat_jid = '15551234567@s.whatsapp.net' BEGIN SELECT RAISE(ABORT, 'synthetic call migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateLIDToPN(lid, pn); err == nil || !strings.Contains(err.Error(), "synthetic call migration failure") {
		t.Fatalf("migration error = %v", err)
	}
	want := []callRow{{ChatJID: lid, SenderJID: lid, CallID: "kept-on-failure"}}
	if got := callRowsOf(t, db, `1 = 1`); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed migration changed calls: %#v", got)
	}
	var count int
	if err := db.sql.QueryRow(`SELECT count(*) FROM chats WHERE jid = ?`, lid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("original chat missing: count=%d err=%v", count, err)
	}
}
