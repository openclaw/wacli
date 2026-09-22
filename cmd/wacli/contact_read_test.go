package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openclaw/wacli/internal/store"
)

const (
	contactPN  = "15550001001@s.whatsapp.net"
	contactLID = "900000001@lid"
)

func TestContactsReadMetadataWithoutCounterpartContactRow(t *testing.T) {
	for _, tc := range []struct{ name, pn, lid, metadataJID string }{
		{"LID only", "15550001003@s.whatsapp.net", "900000003@lid", "15550001003@s.whatsapp.net"},
		{"PN only", "15550001004@s.whatsapp.net", "900000004@lid", "900000004@lid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := seedContactReadStore(t, true)
			db := openSystemImportStore(t, dir)
			if tc.name == "PN only" {
				if err := db.UpsertContact(tc.pn, "15550001004", "", "Phone only", "", ""); err != nil {
					t.Fatal(err)
				}
				session, err := sql.Open("sqlite3", filepath.Join(dir, "session.db"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = session.Exec(`INSERT INTO whatsmeow_lid_map VALUES ('900000004', '15550001004')`)
				session.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := db.SetAlias([]string{tc.metadataJID}, "Counterpart alias"); err != nil {
				t.Fatal(err)
			}
			if err := db.AddTag([]string{tc.metadataJID}, "counterpart tag"); err != nil {
				t.Fatal(err)
			}
			db.Close()
			for _, jid := range []string{tc.pn, tc.lid} {
				got := runContactsShow(t, dir, jid)
				if got.Alias != "Counterpart alias" || got.Name != got.Alias || !reflect.DeepEqual(got.Tags, []string{"counterpart tag"}) {
					t.Errorf("metadata missing through %s: %+v", jid, got)
				}
			}
			if got := runContactsSearch(t, dir, "Counterpart alias"); len(got) != 1 || got[0].JID != tc.pn {
				t.Errorf("counterpart alias search = %+v", got)
			}
			if tc.name == "PN only" {
				db = openSystemImportStore(t, dir)
				if err := db.SetAlias([]string{tc.pn}, "Primary alias"); err != nil {
					t.Fatal(err)
				}
				db.Close()
				if got := runContactsSearch(t, dir, "Counterpart alias"); len(got) != 1 || got[0].Alias != "Primary alias" {
					t.Errorf("hidden counterpart alias search = %+v", got)
				}
			}
		})
	}
}

func TestContactsSearchFoldsOnlyMappedIdentitiesBeforeLimit(t *testing.T) {
	storeDir := seedContactReadStore(t, true)
	contacts := runContactsSearch(t, storeDir, "Alex", "--limit", "2")
	if len(contacts) != 2 || contacts[0].JID != contactPN || contacts[1].JID != "15550001002@s.whatsapp.net" {
		t.Fatalf("expected two distinct contacts after deduplication, got %+v", contacts)
	}
	if contacts[0].Phone != "15550001001" {
		t.Fatalf("canonical phone = %q", contacts[0].Phone)
	}
}

func TestContactsSearchFindsEitherIdentityAndMetadata(t *testing.T) {
	storeDir := seedContactReadStore(t, true)
	db := openSystemImportStore(t, storeDir)
	if err := db.SetAlias([]string{contactLID}, "Device nickname"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSystemName(contactLID, "Imported name"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"Alex Phone", "Alex Device", "Device nickname", "Imported name", contactPN, contactLID} {
		t.Run(query, func(t *testing.T) {
			got := runContactsSearch(t, storeDir, query)
			if len(got) != 1 || got[0].JID != contactPN || got[0].Phone != "15550001001" || got[0].Alias != "Device nickname" || got[0].SystemName != "Imported name" || got[0].Name != "Device nickname" {
				t.Fatalf("merged search result = %+v", got)
			}
		})
	}
}

func TestContactsSearchResolvesLIDOnlyPhone(t *testing.T) {
	storeDir := seedContactReadStore(t, true)
	for _, query := range []string{"Only device", "0001003", "900000003@lid", "15550001003@s.whatsapp.net"} {
		t.Run(query, func(t *testing.T) {
			got := runContactsSearch(t, storeDir, query)
			if len(got) != 1 || got[0].JID != "15550001003@s.whatsapp.net" || got[0].Phone != "15550001003" {
				t.Fatalf("LID-only search result = %+v", got)
			}
		})
	}
}

func TestContactsSearchDoesNotMergeDifferentPeopleWithSameName(t *testing.T) {
	storeDir := seedContactReadStore(t, true)
	db := openSystemImportStore(t, storeDir)
	for _, jid := range []string{contactPN, "15550001002@s.whatsapp.net"} {
		if err := db.SetSystemName(jid, "Same name"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := runContactsSearch(t, storeDir, "Same name"); len(got) != 2 {
		t.Fatalf("different people sharing a name were merged: %+v", got)
	}
}

func TestContactsReadFindsPNOnlyContactByMappedLID(t *testing.T) {
	storeDir := seedContactReadStore(t, true)
	session, err := sql.Open("sqlite3", filepath.Join(storeDir, "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Exec("INSERT INTO whatsmeow_lid_map VALUES ('900000002', '15550001002')"); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	const lid = "900000002@lid"
	got := runContactsSearch(t, storeDir, lid)
	if len(got) != 1 || got[0].JID != "15550001002@s.whatsapp.net" {
		t.Fatalf("PN-only search by LID = %+v", got)
	}
	if got := runContactsShow(t, storeDir, lid); got.JID != "15550001002@s.whatsapp.net" {
		t.Fatalf("PN-only show by LID = %+v", got)
	}
}

func TestContactsReadWithoutMappingDoesNotInventPhoneOrMergeByName(t *testing.T) {
	storeDir := seedContactReadStore(t, false)
	got := runContactsSearch(t, storeDir, "Alex")
	if len(got) != 3 {
		t.Fatalf("unmapped contacts must stay separate: %+v", got)
	}
	for _, c := range got {
		if c.JID == contactLID && c.Phone != "" {
			t.Fatalf("LID reported as phone: %+v", c)
		}
	}
	c := runContactsShow(t, storeDir, contactLID)
	if c.JID != contactLID || c.Phone != "" {
		t.Fatalf("unmapped show = %+v", c)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "session.db")); !os.IsNotExist(err) {
		t.Fatalf("read commands created a session DB: %v", err)
	}
}

func TestContactsShowMergesMetadataWithoutChangingStoredRows(t *testing.T) {
	storeDir := seedContactReadStore(t, true)
	db := openSystemImportStore(t, storeDir)
	for jid, alias := range map[string]string{contactPN: "Primary alias", contactLID: "Device alias"} {
		if err := db.SetAlias([]string{jid}, alias); err != nil {
			t.Fatal(err)
		}
		if err := db.AddTag([]string{jid}, "shared"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSystemName(contactLID, "System name"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddTag([]string{contactPN}, "work"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddTag([]string{contactLID}, "family"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, jid := range []string{contactPN, contactLID} {
		got := runContactsShow(t, storeDir, jid)
		if got.JID != contactPN || got.Phone != "15550001001" || got.Name != "Primary alias" || got.Alias != "Primary alias" || got.SystemName != "System name" || !reflect.DeepEqual(got.Tags, []string{"family", "shared", "work"}) {
			t.Fatalf("show %s = %+v", jid, got)
		}
	}
	if got := runContactsSearch(t, storeDir, "Device alias"); len(got) != 1 || got[0].Alias != "Primary alias" {
		t.Fatalf("nonpreferred alias must remain searchable: %+v", got)
	}
	for _, jid := range []string{"900000003@lid", "15550001003@s.whatsapp.net"} {
		if got := runContactsShow(t, storeDir, jid); got.JID != "15550001003@s.whatsapp.net" || got.Phone != "15550001003" {
			t.Fatalf("LID-only show %s = %+v", jid, got)
		}
	}
	db = openSystemImportStore(t, storeDir)
	defer db.Close()
	stored, err := db.GetContact(contactLID)
	if err != nil || stored.Alias != "Device alias" || !reflect.DeepEqual(stored.Tags, []string{"family", "shared"}) {
		t.Fatalf("read command changed LID metadata: %+v, %v", stored, err)
	}
	rows, err := db.ListContacts(100)
	if err != nil || len(rows) != 4 {
		t.Fatalf("read command changed contact rows: %d, %v", len(rows), err)
	}
}

func seedContactReadStore(t *testing.T, withMappings bool) string {
	t.Helper()
	storeDir := t.TempDir()
	db := openSystemImportStore(t, storeDir)
	for _, c := range []struct{ jid, phone, name string }{
		{contactPN, "15550001001", "Alex Phone"},
		{contactLID, "900000001", "Alex Device"},
		{"15550001002@s.whatsapp.net", "15550001002", "Alex Zed"},
		{"900000003@lid", "900000003", "Only device"},
	} {
		if err := db.UpsertContact(c.jid, c.phone, "", c.name, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if withMappings {
		session, err := sql.Open("sqlite3", filepath.Join(storeDir, "session.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		if _, err := session.Exec(`CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL);
			INSERT INTO whatsmeow_lid_map VALUES ('900000001', '15550001001'), ('900000003', '15550001003');`); err != nil {
			t.Fatal(err)
		}
	}
	return storeDir
}

func runContactsSearch(t *testing.T, storeDir string, args ...string) []store.Contact {
	t.Helper()
	cmd := newContactsSearchCmd(&rootFlags{storeDir: storeDir, readOnly: true, asJSON: true})
	cmd.SetArgs(args)
	raw := captureRootStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("contacts search: %v", err)
		}
	})
	var result struct{ Data []store.Contact }
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}

func runContactsShow(t *testing.T, storeDir, jid string) store.Contact {
	t.Helper()
	cmd := newContactsShowCmd(&rootFlags{storeDir: storeDir, readOnly: true, asJSON: true})
	cmd.SetArgs([]string{"--jid", jid})
	raw := captureRootStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("contacts show: %v", err)
		}
	})
	var result struct{ Data store.Contact }
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}
