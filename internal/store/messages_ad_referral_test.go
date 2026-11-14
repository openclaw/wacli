package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMessageAdReferralRoundTrip(t *testing.T) {
	db := openTestDB(t)

	chat := "lead@s.whatsapp.net"
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Lead", now); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	ref := AdReferral{SourceType: "ad", SourceID: "120200000000000001", Title: "Train with a coach", MediaType: "video", CtwaClid: "clid-1", EntryPointSource: "ctwa_ad"}
	params := UpsertMessageParams{ChatJID: chat, MsgID: "ad1", SenderJID: chat, Timestamp: now, Text: "Hi", AdReferral: &ref}
	if err := db.UpsertMessage(params); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	params.AdReferral = nil
	if err := db.UpsertMessage(params); err != nil {
		t.Fatalf("UpsertMessage replay: %v", err)
	}

	msg, err := db.GetMessage(chat, "ad1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.AdReferral == nil || *msg.AdReferral != ref {
		t.Fatalf("GetMessage AdReferral = %+v, want %+v", msg.AdReferral, ref)
	}
	msgs, err := db.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].AdReferral == nil || *msgs[0].AdReferral != ref {
		t.Fatalf("ListMessages = %+v", msgs)
	}
	encoded, err := json.Marshal(msgs[0])
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"ad_referral":{"source_type":"ad","source_id":"120200000000000001"`) {
		t.Fatalf("JSON output = %s", encoded)
	}

	if err := db.MarkMessageRevoked(chat, "ad1"); err != nil {
		t.Fatalf("MarkMessageRevoked: %v", err)
	}
	if err := db.PurgeMessage(chat, "ad1"); err != nil {
		t.Fatalf("PurgeMessage: %v", err)
	}
	var stored sql.NullString
	if err := db.sql.QueryRow(`SELECT ad_referral FROM messages WHERE chat_jid = ? AND msg_id = ?`, chat, "ad1").Scan(&stored); err != nil {
		t.Fatalf("read purged row: %v", err)
	}
	if stored.Valid {
		t.Fatalf("purged ad_referral = %q, want NULL", stored.String)
	}
}
