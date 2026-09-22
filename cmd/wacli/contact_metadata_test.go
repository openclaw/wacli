package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestContactMetadataRoundTripThroughDisplayedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, input, pn, lid string
	}{
		{"LID only", "15550001003@s.whatsapp.net", "15550001003@s.whatsapp.net", "900000003@lid"},
		{"paired phone", contactPN, contactPN, contactLID},
		{"paired LID", contactLID, contactPN, contactLID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := seedContactReadStore(t, true)
			sessionPath := filepath.Join(dir, "session.db")
			before, err := os.ReadFile(sessionPath)
			if err != nil {
				t.Fatal(err)
			}
			db := openSystemImportStore(t, dir)
			for _, jid := range []string{tc.pn, tc.lid} {
				if err := db.SetAlias([]string{jid}, "old alias"); err != nil {
					t.Fatal(err)
				}
				if err := db.AddTag([]string{jid}, "shared"); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			shown := runContactsShow(t, dir, tc.input)
			if shown.JID != tc.pn {
				t.Fatalf("displayed JID = %s, want %s", shown.JID, tc.pn)
			}
			mutateContactMetadata(t, dir, "alias", "set", tc.input, "--alias", "new alias")
			for _, jid := range []string{tc.pn, tc.lid} {
				if got := runContactsShow(t, dir, jid); got.Alias != "new alias" {
					t.Errorf("alias through %s = %q", jid, got.Alias)
				}
			}
			mutateContactMetadata(t, dir, "alias", "rm", shown.JID)
			mutateContactMetadata(t, dir, "tags", "rm", shown.JID, "--tag", "shared")
			for _, jid := range []string{tc.pn, tc.lid} {
				got := runContactsShow(t, dir, jid)
				if got.Alias != "" || len(got.Tags) != 0 {
					t.Errorf("removed metadata through %s reappeared: %+v", jid, got)
				}
			}
			mutateContactMetadata(t, dir, "tags", "add", shown.JID, "--tag", "new tag")
			if got := runContactsShow(t, dir, tc.lid); len(got.Tags) != 1 || got.Tags[0] != "new tag" {
				t.Errorf("added tag missing: %+v", got)
			}
			db = openSystemImportStore(t, dir)
			defer db.Close()
			rows, err := db.ListContacts(100)
			if err != nil || len(rows) != 4 {
				t.Fatalf("metadata edits changed contact rows: %d, %v", len(rows), err)
			}
			after, err := os.ReadFile(sessionPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("local metadata edit changed the session store: %v", err)
			}
		})
	}
}

func mutateContactMetadata(t *testing.T, dir, kind, operation, jid string, extra ...string) {
	t.Helper()
	flags := &rootFlags{storeDir: dir, asJSON: true}
	cmd := newContactsAliasCmd(flags)
	if kind == "tags" {
		cmd = newContactsTagsCmd(flags)
	}
	cmd.SetArgs(append([]string{operation, "--jid", jid}, extra...))
	captureRootStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestContactMetadataRejectsBrokenIdentityStore(t *testing.T) {
	dir := seedContactReadStore(t, false)
	if err := os.WriteFile(filepath.Join(dir, "session.db"), []byte("not a sqlite database"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newContactsAliasCmd(&rootFlags{storeDir: dir})
	cmd.SetArgs([]string{"set", "--jid", contactPN, "--alias", "must not be written"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("metadata edit ignored a broken identity store")
	}
	db := openSystemImportStore(t, dir)
	defer db.Close()
	got, err := db.GetContact(contactPN)
	if err != nil || got.Alias != "" {
		t.Fatalf("metadata changed on resolver failure: %+v, %v", got, err)
	}
}
