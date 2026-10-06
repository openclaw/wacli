package store

import (
	"path/filepath"
	"testing"
)

func TestUnavailableAppStateKeysPersistAndRemainAccountScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := []byte{1, 2, 3}
	if err := db.MarkAppStateKeyUnavailable(t.Context(), "account-a", id); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for account, want := range map[string]bool{"account-a": true, "account-b": false} {
		got, err := db.IsAppStateKeyUnavailable(t.Context(), account, id)
		if err != nil || got != want {
			t.Fatalf("%s unavailable=%v err=%v", account, got, err)
		}
	}
	if err := db.ClearAppStateKeyUnavailable(t.Context(), "account-a", id); err != nil {
		t.Fatal(err)
	}
	if got, err := db.IsAppStateKeyUnavailable(t.Context(), "account-a", id); err != nil || got {
		t.Fatalf("clear unavailable=%v err=%v", got, err)
	}
}

func TestOpenRepairsMissingUnavailableKeyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TABLE unavailable_app_state_keys`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.MarkAppStateKeyUnavailable(t.Context(), "account-a", []byte{1}); err != nil {
		t.Fatalf("missing table not repaired: %v", err)
	}
}
