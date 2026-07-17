package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenMigratesAppStateRecoveryMarkers(t *testing.T) {
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
		if migration.version >= 21 {
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
	} else if !exists {
		t.Fatal("app_state_recovery_required migration did not run")
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
