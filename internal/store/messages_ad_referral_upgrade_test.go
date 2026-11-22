package store

import (
	"path/filepath"
	"testing"
	"time"
)

// downgradeToBeforeAdReferral rewrites a store the way v0.21.0 left it: no
// ad_referral column, a change trigger that does not compare it, and no record
// of the ad-referral migration.
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
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version = ?`, adReferralMigrationVersion(t)); err != nil {
		t.Fatalf("forget ad-referral migration: %v", err)
	}
}

func adReferralMigrationVersion(t *testing.T) int {
	t.Helper()
	for _, m := range schemaMigrations {
		if m.name == "messages ad_referral column" {
			return m.version
		}
	}
	t.Fatal("ad-referral migration not registered")
	return 0
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

func TestAdReferralMigrationSurvivesOccupiedVersions(t *testing.T) {
	for _, tc := range []struct {
		name           string
		occupyVersion  bool
		columnRetained bool
	}{
		{name: "main 33, stickers 34 and lists 36"},
		{name: "foreign row at referral version", occupyVersion: true},
		{name: "existing column with stale trigger", occupyVersion: true, columnRetained: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db := mustOpen(t, path)
			at := time.Unix(100, 0)
			chat := "lead@s.whatsapp.net"
			if err := db.UpsertChat(chat, "dm", "Lead", at); err != nil {
				t.Fatal(err)
			}
			params := UpsertMessageParams{ChatJID: chat, MsgID: "ad1", Timestamp: at, Text: "Hi"}
			if err := db.UpsertMessage(params); err != nil {
				t.Fatal(err)
			}
			downgradeToBeforeAdReferral(t, db)
			if _, err := db.sql.Exec(`INSERT INTO schema_migrations VALUES(34, 'stickers', 1), (36, 'WhatsApp lists and chat memberships', 1)`); err != nil {
				t.Fatal(err)
			}
			if tc.occupyVersion {
				if _, err := db.sql.Exec(`INSERT INTO schema_migrations VALUES(?, 'foreign migration', 1)`, adReferralMigrationVersion(t)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.columnRetained {
				if _, err := db.sql.Exec(`ALTER TABLE messages ADD COLUMN ad_referral TEXT`); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()

			// Repeated writable opens must repair without reseeding the journal.
			for range 2 {
				db = mustOpen(t, path)
				before, err := db.MessageChanges("beginning", false, 100)
				if err != nil || len(before.Changes) != 1 {
					t.Fatalf("changes after open = %+v, %v", before, err)
				}
				db.Close()
			}
			db = mustOpen(t, path)
			defer db.Close()
			for version, want := range map[int]string{33: "ordered chat auto-unarchive", 34: "stickers", 36: "WhatsApp lists and chat memberships"} {
				var name string
				if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version=?`, version).Scan(&name); err != nil || name != want {
					t.Fatalf("migration %d = %q, %v; want %q", version, name, err, want)
				}
			}
			want := "messages ad_referral column"
			if tc.occupyVersion {
				want = "foreign migration"
			}
			var name string
			if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version=?`, adReferralMigrationVersion(t)).Scan(&name); err != nil || name != want {
				t.Fatalf("referral version owner = %q, %v; want %q", name, err, want)
			}
			checkpoint, err := db.MessageChanges("", true, 1)
			if err != nil {
				t.Fatal(err)
			}
			params.AdReferral = &AdReferral{SourceID: "ad-1"}
			if err := db.UpsertMessage(params); err != nil {
				t.Fatal(err)
			}
			msg, err := db.GetMessage(chat, params.MsgID)
			if err != nil || msg.AdReferral == nil || *msg.AdReferral != *params.AdReferral {
				t.Fatalf("referral after upgrade = %+v, %v", msg.AdReferral, err)
			}
			after, err := db.MessageChanges(checkpoint.NextCheckpoint, false, 100)
			if err != nil || len(after.Changes) != 1 || after.Changes[0].Operation != "upsert" || after.Changes[0].MsgID != params.MsgID {
				t.Fatalf("referral-only changes = %+v, %v", after, err)
			}
		})
	}
}

func TestLocalAdReferral9033UpgradeIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "lead@s.whatsapp.net"
	ref := &AdReferral{SourceID: "local-ad", Title: "Preserved"}
	if err := db.UpsertChat(chat, "dm", "Lead", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "local", Timestamp: time.Unix(100, 0), Text: "Hi", AdReferral: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE schema_migrations SET version=9033 WHERE version=?`, adReferralMigrationVersion(t)); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := db.MessageChanges("", true, 1)
	if err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.sql.QueryRow(`SELECT revision FROM retrieval_state WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	// The migration itself must not rewrite schema or payloads when the local
	// build already installed the column and referral-aware change trigger.
	var beforeSchema, afterSchema int
	if err := db.sql.QueryRow(`PRAGMA schema_version`).Scan(&beforeSchema); err != nil {
		t.Fatal(err)
	}
	if err := migrateMessagesAdReferralColumn(db); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`PRAGMA schema_version`).Scan(&afterSchema); err != nil || afterSchema != beforeSchema {
		t.Fatalf("migration rewrote schema: %d -> %d, %v", beforeSchema, afterSchema, err)
	}
	db.Close()
	for range 2 {
		db = mustOpen(t, path)
		msg, err := db.GetMessage(chat, "local")
		if err != nil || msg.AdReferral == nil || *msg.AdReferral != *ref {
			t.Fatalf("local referral = %+v, %v", msg.AdReferral, err)
		}
		changes, err := db.MessageChanges(checkpoint.NextCheckpoint, false, 100)
		if err != nil || changes.StoreID != checkpoint.StoreID || len(changes.Changes) != 0 {
			t.Fatalf("local journal changed = %+v, %v", changes, err)
		}
		var gotRevision int64
		if err := db.sql.QueryRow(`SELECT revision FROM retrieval_state WHERE id=1`).Scan(&gotRevision); err != nil || gotRevision != revision {
			t.Fatalf("revision = %d, %v; want %d", gotRevision, err, revision)
		}
		for _, version := range []int{9033, adReferralMigrationVersion(t)} {
			var name string
			if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version=?`, version).Scan(&name); err != nil || name != "messages ad_referral column" {
				t.Fatalf("migration %d = %q, %v", version, name, err)
			}
		}
		db.Close()
	}
}

func TestLegacyAdReferral33AlsoUpgradesAutoUnarchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "lead@s.whatsapp.net"
	at := time.Unix(100, 0)
	if err := db.UpsertChat(chat, "dm", "Lead", at); err != nil {
		t.Fatal(err)
	}
	ref := &AdReferral{SourceID: "legacy-ad"}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "legacy", Timestamp: at, Text: "Hi", AdReferral: ref}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(chat, true); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the old PR head: referrals at 33, no archive boundary
	// columns or preference table. The number-only runner must skip main's 33.
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version=?;
		UPDATE schema_migrations SET name='messages ad_referral column' WHERE version=33;
		ALTER TABLE chats DROP COLUMN archive_action_ms;
		ALTER TABLE chats DROP COLUMN archive_incoming_ms;
		ALTER TABLE chats DROP COLUMN archive_cleared_ms;
		ALTER TABLE chats DROP COLUMN archive_setting_min_ms;
		DROP TABLE chat_archive_setting`, adReferralMigrationVersion(t)); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := db.MessageChanges("", true, 1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	for range 2 {
		db = mustOpen(t, path)
		if required, err := db.ChatArchiveRecoveryRequired(); err != nil || !required {
			t.Fatalf("legacy archive recovery = %v, %v; want unknown preference", required, err)
		}
		msg, err := db.GetMessage(chat, "legacy")
		if err != nil || msg.AdReferral == nil || *msg.AdReferral != *ref {
			t.Fatalf("legacy referral = %+v, %v", msg.AdReferral, err)
		}
		changes, err := db.MessageChanges(checkpoint.NextCheckpoint, false, 100)
		if err != nil || changes.StoreID != checkpoint.StoreID || len(changes.Changes) != 0 {
			t.Fatalf("legacy journal changed = %+v, %v", changes, err)
		}
		db.Close()
	}
	db = mustOpen(t, path)
	defer db.Close()
	var name string
	if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version=33`).Scan(&name); err != nil || name != "messages ad_referral column" {
		t.Fatalf("legacy marker = %q, %v", name, err)
	}
	if err := db.SetChatArchiveSetting(true, at); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyChatArchive(chat, true, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "incoming", Timestamp: at.Add(2 * time.Second), Text: "New", ArchiveIncoming: true, AdReferral: ref}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChat(chat)
	if err != nil || got.Archived {
		t.Fatalf("upgraded auto-unarchive = %+v, %v", got, err)
	}
}
