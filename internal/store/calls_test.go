package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUpsertCallEventUpdatesTimestampForSameCall(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	oldTS := time.Date(1970, 1, 21, 14, 17, 15, 0, time.UTC)
	newTS := time.Date(2026, 5, 22, 10, 45, 0, 0, time.UTC)
	chatJID := "15551234567@s.whatsapp.net"
	if err := db.UpsertChat(chatJID, "dm", "Alice", oldTS); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	base := UpsertCallEventParams{
		ChatJID:   chatJID,
		CallID:    "call-1",
		EventType: "call_log",
		Direction: "outbound",
		Media:     "audio",
		Outcome:   "connected",
		Timestamp: oldTS,
	}
	if err := db.UpsertCallEvent(base); err != nil {
		t.Fatalf("UpsertCallEvent old: %v", err)
	}
	base.Timestamp = newTS
	base.DurationSecs = 9
	if err := db.UpsertCallEvent(base); err != nil {
		t.Fatalf("UpsertCallEvent new: %v", err)
	}

	calls, err := db.ListCallEvents(ListCallEventsParams{ChatJID: base.ChatJID, Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	if !calls[0].Timestamp.Equal(newTS) || calls[0].DurationSecs != 9 {
		t.Fatalf("call = %+v, want timestamp %s and duration 9", calls[0], newTS)
	}
}

func TestUpsertCallEventDoesNotMoveOtherChatsWithSameCallID(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	oldTS := time.Date(2026, 5, 22, 10, 45, 0, 0, time.UTC)
	newTS := oldTS.Add(time.Hour)
	chatA := "15551234567@s.whatsapp.net"
	chatB := "15557654321@s.whatsapp.net"
	for _, chat := range []string{chatA, chatB} {
		if err := db.UpsertChat(chat, "dm", "", oldTS); err != nil {
			t.Fatalf("UpsertChat %s: %v", chat, err)
		}
		if err := db.UpsertCallEvent(UpsertCallEventParams{
			ChatJID:   chat,
			CallID:    "duplicated-call-id",
			EventType: "call_log",
			Timestamp: oldTS,
		}); err != nil {
			t.Fatalf("UpsertCallEvent %s: %v", chat, err)
		}
	}

	if err := db.UpsertCallEvent(UpsertCallEventParams{
		ChatJID:      chatA,
		CallID:       "duplicated-call-id",
		EventType:    "call_log",
		DurationSecs: 12,
		Timestamp:    newTS,
		Participants: []CallParticipant{{JID: chatB}},
	}); err != nil {
		t.Fatalf("UpsertCallEvent update: %v", err)
	}

	callsA, err := db.ListCallEvents(ListCallEventsParams{ChatJID: chatA, Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents chatA: %v", err)
	}
	if len(callsA) != 1 || !callsA[0].Timestamp.Equal(newTS) || callsA[0].DurationSecs != 12 {
		t.Fatalf("chatA calls = %+v, want one updated row", callsA)
	}
	callsB, err := db.ListCallEvents(ListCallEventsParams{ChatJID: chatB, Limit: 10})
	if err != nil {
		t.Fatalf("ListCallEvents chatB: %v", err)
	}
	if len(callsB) != 1 || !callsB[0].Timestamp.Equal(oldTS) || callsB[0].DurationSecs != 0 {
		t.Fatalf("chatB calls = %+v, want original row untouched", callsB)
	}
}
