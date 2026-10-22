package store

import (
	"testing"
	"time"
)

func TestRepairMisfiledRevokes(t *testing.T) {
	db := openTestDB(t)
	const owner, contact, group = "15550100001@s.whatsapp.net", "15550100002@s.whatsapp.net", "120363001234567890@g.us"
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for jid, kind := range map[string]string{owner: "dm", contact: "dm", group: "group"} {
		if err := db.UpsertChat(jid, kind, jid, t0); err != nil {
			t.Fatal(err)
		}
	}
	put := func(p UpsertMessageParams) {
		t.Helper()
		if err := db.UpsertMessage(p); err != nil {
			t.Fatal(err)
		}
	}
	put(UpsertMessageParams{ChatJID: contact, MsgID: "m1", SenderJID: contact, Timestamp: t0, Text: "keep me", DisplayText: "keep me", MediaType: "image", DirectPath: "/p"})
	// Misfiled revoke as produced by the old parser.
	put(UpsertMessageParams{ChatJID: owner, MsgID: "m1", SenderJID: contact, Timestamp: t0.Add(time.Hour), FromMe: true, Revoked: true, DisplayText: DeletedMessageDisplayText})
	// Legit owner revoke in a contact chat and in a group: must be untouched.
	put(UpsertMessageParams{ChatJID: contact, MsgID: "m2", SenderJID: owner, Timestamp: t0, FromMe: true, Text: "mine"})
	put(UpsertMessageParams{ChatJID: contact, MsgID: "m2", SenderJID: owner, Timestamp: t0, FromMe: true, Revoked: true})
	put(UpsertMessageParams{ChatJID: group, MsgID: "g1", SenderJID: contact, Timestamp: t0, Revoked: true})

	found, err := db.FindMisfiledRevokes()
	if err != nil || len(found) != 1 || found[0].MsgID != "m1" || found[0].TargetChatJID != contact {
		t.Fatalf("found = %+v, err %v", found, err)
	}
	n, err := db.RepairMisfiledRevokes()
	if err != nil || n != 1 {
		t.Fatalf("repair = %d, %v", n, err)
	}
	m, err := db.GetMessage(contact, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Revoked || m.DeletedAt == nil || m.DeletionReason != MessageDeletionReasonWhatsAppRevoke || m.Text != "keep me" || m.MediaType != "image" || m.DirectPath != "/p" {
		t.Fatalf("original not repaired intact: %+v", m)
	}
	if _, err := db.GetMessage(owner, "m1"); err == nil {
		t.Fatalf("misfiled row still present")
	}
	if _, err := db.GetMessage(contact, "m2"); err != nil {
		t.Fatalf("legit row touched: %v", err)
	}
	if again, _ := db.FindMisfiledRevokes(); len(again) != 0 {
		t.Fatalf("repair not idempotent: %+v", again)
	}
}
