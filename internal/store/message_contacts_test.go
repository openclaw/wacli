package store

import (
	"testing"
	"time"
)

const testVCard = "BEGIN:VCARD\nVERSION:3.0\nFN:Ana Prueba\nTEL;type=CELL;waid=51900000001:+51 900 000 001\nEND:VCARD"

func seedContactMessage(t *testing.T, db *DB, chat, msgID string) {
	t.Helper()
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Chat", ts); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{
		ChatJID: chat, MsgID: msgID, Timestamp: ts, DisplayText: "Contact: Ana Prueba (+51 900 000 001)",
	}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
}

func TestMessageContactsRoundTripKeepsOrderAndReplaces(t *testing.T) {
	db := openTestDB(t)
	chat := "15551234567@s.whatsapp.net"
	seedContactMessage(t, db, chat, "C1")

	if err := db.ReplaceMessageContacts(chat, "C1", []MessageContact{
		{DisplayName: "Ana Prueba", VCard: testVCard},
		{DisplayName: "", VCard: "BEGIN:VCARD\nFN:Sin numero\nEND:VCARD"},
		{DisplayName: "Vacia", VCard: "  "},
	}); err != nil {
		t.Fatalf("ReplaceMessageContacts: %v", err)
	}
	got, err := db.GetMessageContacts(chat, "C1")
	if err != nil {
		t.Fatalf("GetMessageContacts: %v", err)
	}
	if len(got) != 2 || got[0].VCard != testVCard || got[0].DisplayName != "Ana Prueba" || got[1].Position != 1 || got[1].DisplayName != "" {
		t.Fatalf("cards = %+v", got)
	}

	if err := db.ReplaceMessageContacts(chat, "C1", []MessageContact{{VCard: "BEGIN:VCARD\nFN:Otra\nEND:VCARD"}}); err != nil {
		t.Fatalf("second ReplaceMessageContacts: %v", err)
	}
	got, _ = db.GetMessageContacts(chat, "C1")
	if len(got) != 1 || got[0].VCard != "BEGIN:VCARD\nFN:Otra\nEND:VCARD" {
		t.Fatalf("cards after replace = %+v", got)
	}

	// No cards leaves what is stored.
	if err := db.ReplaceMessageContacts(chat, "C1", nil); err != nil {
		t.Fatalf("empty ReplaceMessageContacts: %v", err)
	}
	if got, _ = db.GetMessageContacts(chat, "C1"); len(got) != 1 {
		t.Fatalf("empty replace dropped cards: %+v", got)
	}
}

func TestMessageContactsAreDroppedWithThePayloadAndNotStoredAgain(t *testing.T) {
	db := openTestDB(t)
	chat := "15551234567@s.whatsapp.net"
	seedContactMessage(t, db, chat, "C1")
	if err := db.ReplaceMessageContacts(chat, "C1", []MessageContact{{VCard: testVCard}}); err != nil {
		t.Fatalf("ReplaceMessageContacts: %v", err)
	}
	if err := db.MarkMessageRevoked(chat, "C1"); err != nil {
		t.Fatalf("MarkMessageRevoked: %v", err)
	}
	if err := db.PurgeMessage(chat, "C1"); err != nil {
		t.Fatalf("PurgeMessage: %v", err)
	}
	if got, _ := db.GetMessageContacts(chat, "C1"); len(got) != 0 {
		t.Fatalf("purged message kept its vCards: %+v", got)
	}
	if err := db.ReplaceMessageContacts(chat, "C1", []MessageContact{{VCard: testVCard}}); err != nil {
		t.Fatalf("ReplaceMessageContacts after purge: %v", err)
	}
	if got, _ := db.GetMessageContacts(chat, "C1"); len(got) != 0 {
		t.Fatalf("purged message stored its vCards again: %+v", got)
	}
}

func TestMessageContactsLeaveWithTheirChat(t *testing.T) {
	db := openTestDB(t)
	chat := "15551234567@s.whatsapp.net"
	other := "15557654321@s.whatsapp.net"
	for _, jid := range []string{chat, other} {
		seedContactMessage(t, db, jid, "C1")
		if err := db.ReplaceMessageContacts(jid, "C1", []MessageContact{{VCard: testVCard}}); err != nil {
			t.Fatalf("ReplaceMessageContacts: %v", err)
		}
	}
	if err := db.DeleteChat(chat); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	if got, _ := db.GetMessageContacts(chat, "C1"); len(got) != 0 {
		t.Fatalf("deleted chat kept its vCards: %+v", got)
	}
	if got, _ := db.GetMessageContacts(other, "C1"); len(got) != 1 {
		t.Fatalf("other chat lost its vCards: %+v", got)
	}
}

func TestMigrateLIDToPNMovesMessageContacts(t *testing.T) {
	db := openTestDB(t)
	pn := "15551234567@s.whatsapp.net"
	lid := "999123456789@lid"
	seedContactMessage(t, db, lid, "C1")
	if err := db.ReplaceMessageContacts(lid, "C1", []MessageContact{{DisplayName: "Ana Prueba", VCard: testVCard}}); err != nil {
		t.Fatalf("ReplaceMessageContacts: %v", err)
	}
	if err := db.MigrateLIDToPN(lid, pn); err != nil {
		t.Fatalf("MigrateLIDToPN: %v", err)
	}
	if got, _ := db.GetMessageContacts(pn, "C1"); len(got) != 1 || got[0].VCard != testVCard {
		t.Fatalf("pn cards = %+v", got)
	}
	if got, _ := db.GetMessageContacts(lid, "C1"); len(got) != 0 {
		t.Fatalf("lid cards retained: %+v", got)
	}
}
