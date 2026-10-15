package store

import (
	"path/filepath"
	"testing"
	"time"
)

// downgradeToBeforeAdReferral rewrites a store the way v0.21.0 left it: no
// ad_referral column, a change trigger that does not compare it, and no record
// of migration 33.
func downgradeToBeforeAdReferral(t *testing.T, db *DB) {
	t.Helper()
	for _, name := range []string{"retrieval_messages_insert", "retrieval_messages_update", "retrieval_messages_delete"} {
		if _, err := db.sql.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
	if _, err := db.sql.Exec(`ALTER TABLE messages DROP COLUMN ad_referral`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := createMessageChangeTriggers(tx); err != nil {
		t.Fatalf("recreate triggers: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version = 33`); err != nil {
		t.Fatalf("forget migration 33: %v", err)
	}
}

func TestReadOnlyStoreBeforeAdReferralStillAnswersEveryReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "lead@s.whatsapp.net"
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Lead", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	for i, id := range []string{"m1", "m2", "m3"} {
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: id, SenderJID: chat, Timestamp: base.Add(time.Duration(i) * time.Minute), Text: "hello " + id}); err != nil {
			t.Fatalf("UpsertMessage: %v", err)
		}
	}
	if err := db.SetStarred(SetStarredParams{ChatJID: chat, MsgID: "m2", SenderJID: chat, Starred: true, StarredAt: base}); err != nil {
		t.Fatalf("SetStarred: %v", err)
	}
	downgradeToBeforeAdReferral(t, db)
	db.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	listed, err := ro.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 10})
	if err != nil || len(listed) != 3 {
		t.Fatalf("ListMessages = %d rows, err = %v; want 3", len(listed), err)
	}
	searched, err := ro.SearchMessages(SearchMessagesParams{Query: "hello", Limit: 10})
	if err != nil || len(searched) != 3 {
		t.Fatalf("SearchMessages = %d rows, err = %v; want 3", len(searched), err)
	}
	starred, err := ro.ListStarredMessages(ListStarredMessagesParams{Limit: 10})
	if err != nil || len(starred) != 1 || starred[0].MsgID != "m2" {
		t.Fatalf("ListStarredMessages = %+v, err = %v; want m2", starred, err)
	}
	got, err := ro.GetMessage(chat, "m2")
	if err != nil || got.Text != "hello m2" || !got.Starred || got.AdReferral != nil {
		t.Fatalf("GetMessage = %+v, err = %v", got, err)
	}
	around, err := ro.MessageContext(chat, "m2", 1, 1)
	if err != nil || len(around) != 3 || around[0].MsgID != "m1" || around[2].MsgID != "m3" {
		t.Fatalf("MessageContext = %+v, err = %v", around, err)
	}
	for _, m := range listed {
		if m.AdReferral != nil {
			t.Fatalf("message %s has an ad referral on a store without the column", m.MsgID)
		}
	}
}

func TestReferralOnlyEnrichmentIsJournaledAfterUpgradingFromBeforeAdReferral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "lead@s.whatsapp.net"
	base := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Lead", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	params := UpsertMessageParams{ChatJID: chat, MsgID: "ad1", SenderJID: chat, Timestamp: base, Text: "Hi"}
	if err := db.UpsertMessage(params); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
	downgradeToBeforeAdReferral(t, db)
	db.Close()

	db = mustOpen(t, path)
	defer db.Close()
	before, err := db.MessageChanges("beginning", false, 100)
	if err != nil {
		t.Fatalf("MessageChanges before: %v", err)
	}
	checkpoint, err := db.MessageChanges("", true, 1)
	if err != nil {
		t.Fatalf("MessageChanges checkpoint: %v", err)
	}
	var revisionBefore int64
	if err := db.sql.QueryRow(`SELECT revision FROM retrieval_state WHERE id=1`).Scan(&revisionBefore); err != nil {
		t.Fatalf("read revision: %v", err)
	}

	ref := AdReferral{SourceType: "ad", SourceID: "120200000000000001"}
	params.AdReferral = &ref
	if err := db.UpsertMessage(params); err != nil {
		t.Fatalf("UpsertMessage with referral: %v", err)
	}

	var revisionAfter int64
	if err := db.sql.QueryRow(`SELECT revision FROM retrieval_state WHERE id=1`).Scan(&revisionAfter); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if revisionAfter <= revisionBefore {
		t.Fatalf("retrieval revision %d did not advance past %d", revisionAfter, revisionBefore)
	}
	after, err := db.MessageChanges(checkpoint.NextCheckpoint, false, 100)
	if err != nil {
		t.Fatalf("MessageChanges after: %v", err)
	}
	if after.StoreID != before.StoreID {
		t.Fatalf("store id changed from %q to %q", before.StoreID, after.StoreID)
	}
	if len(after.Changes) != 1 || after.Changes[0].Operation != "upsert" || after.Changes[0].MsgID != "ad1" {
		t.Fatalf("changes after enrichment = %+v; want one upsert of ad1", after.Changes)
	}
	if len(before.Changes) != 1 {
		t.Fatalf("upgrade reseeded the journal: %d entries before enrichment", len(before.Changes))
	}
}
