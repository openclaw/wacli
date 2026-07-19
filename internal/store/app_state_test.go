package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenCreatesAppStateRecoveryIntents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(coreSchemaSQL + `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	for _, migration := range schemaMigrations {
		if migration.version >= 23 {
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, migration.version, migration.name); err != nil {
			_ = raw.Close()
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy DB: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated DB: %v", err)
	}
	defer db.Close()
	if exists, err := db.tableExists("app_state_recovery_required"); err != nil {
		t.Fatalf("tableExists: %v", err)
	} else if exists {
		t.Fatal("legacy app_state_recovery_required table still exists")
	}
	if exists, err := db.tableExists("app_state_recovery_intents"); err != nil {
		t.Fatalf("intent tableExists: %v", err)
	} else if !exists {
		t.Fatal("app_state_recovery_intents migration did not run")
	}
}

func TestOpenReconcilesPreReleaseAppStateMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(coreSchemaSQL + `
		DROP TABLE message_local_media_aliases;
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
		CREATE TABLE app_state_recovery_required (
			collection TEXT PRIMARY KEY,
			marked_at INTEGER NOT NULL
		);
		CREATE TABLE app_state_recovery_intents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			collection TEXT NOT NULL
		);
		CREATE INDEX idx_app_state_recovery_intents_collection
		ON app_state_recovery_intents(collection);
		INSERT INTO chats(jid, kind, name)
		VALUES('chat@s.whatsapp.net', 'dm', 'Migration test');
		INSERT INTO messages(chat_jid, msg_id, ts, from_me, text, revoked)
		VALUES('chat@s.whatsapp.net', 'legacy-delete', 123, 1, 'retained text', 1);
		INSERT INTO app_state_recovery_required(collection, marked_at)
		VALUES('regular_low', 1), ('regular_high', 1);
		INSERT INTO app_state_recovery_intents(collection)
		VALUES('regular_low');
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create pre-release schema: %v", err)
	}
	for _, migration := range schemaMigrations {
		if migration.version >= 21 {
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, migration.version, migration.name); err != nil {
			_ = raw.Close()
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}
	if _, err := raw.Exec(`
		INSERT INTO schema_migrations(version, name, applied_at)
		VALUES(21, 'app state recovery markers', 1),
		      (22, 'app state recovery intents', 1)
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("record pre-release migrations: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close pre-release DB: %v", err)
	}

	assertMigrated := func(db *DB) {
		t.Helper()
		if exists, err := db.tableExists("app_state_recovery_required"); err != nil {
			t.Fatalf("legacy tableExists: %v", err)
		} else if exists {
			t.Fatal("legacy recovery marker table still exists")
		}
		if exists, err := db.tableExists("message_local_media_aliases"); err != nil {
			t.Fatalf("media aliases tableExists: %v", err)
		} else if !exists {
			t.Fatal("message local media aliases migration was not reconciled")
		}
		for _, collection := range []string{"regular_low", "regular_high"} {
			if got := countRows(t, db.sql, `SELECT COUNT(*) FROM app_state_recovery_intents WHERE collection = ?`, collection); got != 1 {
				t.Fatalf("recovery intents for %s = %d, want 1", collection, got)
			}
		}
		msg, err := db.GetMessage("chat@s.whatsapp.net", "legacy-delete")
		if err != nil {
			t.Fatalf("GetMessage: %v", err)
		}
		if msg.Text != "retained text" || msg.DeletedAt == nil || msg.DeletedAt.Unix() != 123 || msg.DeletionReason != "legacy-whatsapp-revoke" {
			t.Fatalf("reconciled tombstone = %+v", msg)
		}
		var migrationName string
		if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version = 24`).Scan(&migrationName); err != nil {
			t.Fatalf("load migration 24: %v", err)
		}
		if migrationName != "app state recovery intents" {
			t.Fatalf("migration 24 name = %q", migrationName)
		}
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated DB: %v", err)
	}
	assertMigrated(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated DB: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen migrated DB: %v", err)
	}
	defer db.Close()
	assertMigrated(db)
}

func TestOpenReconcilesPreReleaseVersion22Collision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := raw.Exec(coreSchemaSQL + `
		DROP TABLE message_local_media_aliases;
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		);
		CREATE TABLE app_state_recovery_intents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			collection TEXT NOT NULL
		);
		CREATE INDEX idx_app_state_recovery_intents_collection
		ON app_state_recovery_intents(collection);
		INSERT INTO app_state_recovery_intents(collection)
		VALUES('regular_low');
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create pre-release schema: %v", err)
	}
	for _, migration := range schemaMigrations {
		if migration.version >= 22 {
			continue
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, migration.version, migration.name); err != nil {
			_ = raw.Close()
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}
	if _, err := raw.Exec(`
		INSERT INTO schema_migrations(version, name, applied_at)
		VALUES(22, 'app state recovery markers', 1),
		      (23, 'app state recovery intents', 1)
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("record pre-release migrations: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close pre-release DB: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open migrated DB: %v", err)
	}
	defer db.Close()
	if exists, err := db.tableExists("message_local_media_aliases"); err != nil || !exists {
		t.Fatalf("message_local_media_aliases table exists = %v, err = %v", exists, err)
	}
	if exists, err := db.tableExists("app_state_recovery_required"); err != nil {
		t.Fatalf("legacy tableExists: %v", err)
	} else if exists {
		t.Fatal("legacy recovery marker table still exists")
	}
	if got := countRows(t, db.sql, `SELECT COUNT(*) FROM app_state_recovery_intents WHERE collection = 'regular_low'`); got != 1 {
		t.Fatalf("regular_low recovery intents = %d, want 1", got)
	}
	var migrationName string
	if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version = 24`).Scan(&migrationName); err != nil {
		t.Fatalf("load migration 24: %v", err)
	}
	if migrationName != "app state recovery intents" {
		t.Fatalf("migration 24 name = %q", migrationName)
	}
}

func TestAppStateRecoveryMarkerPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.MarkAppStateRecoveryRequired("regular_low"); err != nil {
		t.Fatalf("MarkAppStateRecoveryRequired: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	required, err := db.AppStateRecoveryRequired("regular_low")
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if !required {
		t.Fatal("recovery marker was not durable")
	}
	if err := db.ClearAppStateRecoveryRequired("regular_low"); err != nil {
		t.Fatalf("ClearAppStateRecoveryRequired: %v", err)
	}
	required, err = db.AppStateRecoveryRequired("regular_low")
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired after clear: %v", err)
	}
	if required {
		t.Fatal("recovery marker remained after clear")
	}
}

func TestAppStateRecoveryMarkersAreCollectionScoped(t *testing.T) {
	db := openTestDB(t)
	if err := db.MarkAppStateRecoveryRequired("regular_high"); err != nil {
		t.Fatalf("MarkAppStateRecoveryRequired: %v", err)
	}
	for collection, want := range map[string]bool{
		"regular_high": true,
		"regular_low":  false,
	} {
		got, err := db.AppStateRecoveryRequired(collection)
		if err != nil {
			t.Fatalf("AppStateRecoveryRequired(%q): %v", collection, err)
		}
		if got != want {
			t.Fatalf("AppStateRecoveryRequired(%q) = %t, want %t", collection, got, want)
		}
	}
}

func TestAppStateRecoveryGenerationDoesNotClearNewFailure(t *testing.T) {
	db := openTestDB(t)
	generation, alreadyRequired, err := db.BeginAppStateRecovery("regular_low")
	if err != nil {
		t.Fatalf("BeginAppStateRecovery: %v", err)
	}
	if alreadyRequired || generation != 1 {
		t.Fatalf("initial marker = (%d, %t), want (1, false)", generation, alreadyRequired)
	}
	if err := db.MarkAppStateRecoveryRequired("regular_low"); err != nil {
		t.Fatalf("MarkAppStateRecoveryRequired: %v", err)
	}
	cleared, err := db.ClearAppStateRecoveryGeneration("regular_low", generation)
	if err != nil {
		t.Fatalf("ClearAppStateRecoveryGeneration stale: %v", err)
	}
	if cleared {
		t.Fatal("stale recovery generation cleared a newer failure")
	}
	generation, alreadyRequired, err = db.BeginAppStateRecovery("regular_low")
	if err != nil {
		t.Fatalf("BeginAppStateRecovery existing: %v", err)
	}
	if !alreadyRequired || generation != 3 {
		t.Fatalf("existing marker = (%d, %t), want (3, true)", generation, alreadyRequired)
	}
	cleared, err = db.ClearAppStateRecoveryGeneration("regular_low", generation)
	if err != nil {
		t.Fatalf("ClearAppStateRecoveryGeneration current: %v", err)
	}
	if !cleared {
		t.Fatal("current recovery generation was not cleared")
	}
}

func TestAppStateRecoveryIntentClearsOnlyItsOwnSuccess(t *testing.T) {
	db := openTestDB(t)
	failed, err := db.MarkAppStateRecoveryGeneration("regular_low")
	if err != nil {
		t.Fatalf("MarkAppStateRecoveryGeneration failed: %v", err)
	}
	succeeded, err := db.MarkAppStateRecoveryGeneration("regular_low")
	if err != nil {
		t.Fatalf("MarkAppStateRecoveryGeneration succeeded: %v", err)
	}
	if err := db.ClearAppStateRecoveryIntent("regular_low", succeeded); err != nil {
		t.Fatalf("ClearAppStateRecoveryIntent succeeded: %v", err)
	}
	required, err := db.AppStateRecoveryRequired("regular_low")
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired after success: %v", err)
	}
	if !required {
		t.Fatal("successful live intent cleared an earlier failed intent")
	}
	if err := db.ClearAppStateRecoveryIntent("regular_low", failed); err != nil {
		t.Fatalf("ClearAppStateRecoveryIntent failed: %v", err)
	}
	required, err = db.AppStateRecoveryRequired("regular_low")
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired after all clears: %v", err)
	}
	if required {
		t.Fatal("recovery intent remained after every intent cleared")
	}
}

func TestAppStateRecoveryIntentBatchIsAtomic(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.MarkAppStateRecoveryGenerations([]string{"regular_low", ""}); err == nil {
		t.Fatal("MarkAppStateRecoveryGenerations accepted an empty collection")
	}
	required, err := db.AppStateRecoveryRequired("regular_low")
	if err != nil {
		t.Fatalf("AppStateRecoveryRequired: %v", err)
	}
	if required {
		t.Fatal("failed marker batch committed a partial recovery intent")
	}
}
