package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/store"
)

func TestReadSystemContactsPreservesTenMiBInputLimit(t *testing.T) {
	input := filepath.Join(t.TempDir(), "contacts.json")
	const contact = `[{"full_name":"Alice","phones":["+15551234567"]}]`
	data := contact + strings.Repeat(" ", 10*1024*1024-len(contact))
	if err := os.WriteFile(input, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	contacts, err := readSystemContacts(context.Background(), input)
	if err != nil || len(contacts) != 1 {
		t.Fatalf("10 MiB import: contacts=%v err=%v", contacts, err)
	}
	if err := os.WriteFile(input, []byte(data+" "), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSystemContacts(context.Background(), input); err == nil {
		t.Fatal("expected oversized file to be rejected")
	}
}

func TestContactsImportSystemFromInputDryRunDoesNotWrite(t *testing.T) {
	storeDir, input := seedSystemImportStore(t)

	cmd := newContactsImportSystemCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	cmd.SetArgs([]string{"--input", input, "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("contacts import-system dry-run: %v", err)
	}

	db := openSystemImportStore(t, storeDir)
	defer db.Close()
	c, err := db.GetContact("14157347847@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.SystemName != "" {
		t.Fatalf("dry-run wrote system name: %#v", c)
	}
}

func TestContactsImportSystemFromInputWritesAndClears(t *testing.T) {
	storeDir, input := seedSystemImportStore(t)

	cmd := newContactsImportSystemCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	cmd.SetArgs([]string{"--input", input})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("contacts import-system: %v", err)
	}

	db := openSystemImportStore(t, storeDir)
	c, err := db.GetContact("14157347847@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.SystemName != "Alice Appleseed" || c.Name != "Alice Appleseed" {
		t.Fatalf("contact = %#v", c)
	}
	_ = db.Close()

	clearCmd := newContactsImportSystemCmd(&rootFlags{storeDir: storeDir, timeout: time.Minute})
	clearCmd.SetArgs([]string{"--clear"})
	if err := clearCmd.Execute(); err != nil {
		t.Fatalf("contacts import-system --clear: %v", err)
	}
	db = openSystemImportStore(t, storeDir)
	defer db.Close()
	c, err = db.GetContact("14157347847@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetContact after clear: %v", err)
	}
	if c.SystemName != "" {
		t.Fatalf("clear left system name: %#v", c)
	}
}

func seedSystemImportStore(t *testing.T) (string, string) {
	t.Helper()
	storeDir := t.TempDir()
	db := openSystemImportStore(t, storeDir)
	if err := db.UpsertContact("14157347847@s.whatsapp.net", "14157347847", "WhatsApp Alice", "", "", ""); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	input := filepath.Join(storeDir, "contacts.json")
	raw, err := json.Marshal([]map[string]any{
		{"full_name": "Alice Appleseed", "phones": []string{"+1 (415) 734-7847"}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := fsutil.WritePrivateFile(input, raw); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return storeDir, input
}

func openSystemImportStore(t *testing.T, storeDir string) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}
