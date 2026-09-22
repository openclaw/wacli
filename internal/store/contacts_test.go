package store

import (
	"testing"
)

func TestContactMetadataUpdateRollsBackAllIdentities(t *testing.T) {
	db := openTestDB(t)
	jids := []string{"111@s.whatsapp.net", "222@lid"}
	for _, jid := range jids {
		if err := db.UpsertContact(jid, "", "", "Contact", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetAlias(jids, "original"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`CREATE TRIGGER reject_alias BEFORE INSERT ON contact_aliases
		WHEN NEW.jid = '222@lid' BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAlias(jids, "replacement"); err == nil {
		t.Fatal("expected alias update to fail")
	}
	for _, jid := range jids {
		got, err := db.GetContact(jid)
		if err != nil || got.Alias != "original" {
			t.Fatalf("partial alias update for %s: %+v, %v", jid, got, err)
		}
	}
}

func TestContactsAliasTagsAndSearch(t *testing.T) {
	db := openTestDB(t)

	jid := "111@s.whatsapp.net"
	if err := db.UpsertContact(jid, "111", "Push", "Full Name", "First", "Biz"); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}
	if err := db.SetAlias([]string{jid}, "Ali"); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}
	if err := db.AddTag([]string{jid}, "friends"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}
	if err := db.AddTag([]string{jid}, "work"); err != nil {
		t.Fatalf("AddTag: %v", err)
	}

	c, err := db.GetContact(jid)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.Alias != "Ali" {
		t.Fatalf("expected alias Ali, got %q", c.Alias)
	}
	if len(c.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %v", c.Tags)
	}

	found, err := db.SearchContacts("Ali", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(found) != 1 || found[0].JID != jid {
		t.Fatalf("expected to find contact by alias, got %+v", found)
	}

	for _, query := range []string{"First", "Biz"} {
		found, err := db.SearchContacts(query, 10)
		if err != nil {
			t.Fatalf("SearchContacts %q: %v", query, err)
		}
		if len(found) != 1 || found[0].JID != jid {
			t.Fatalf("expected to find contact by %q, got %+v", query, found)
		}
	}

	if err := db.RemoveTag([]string{jid}, "work"); err != nil {
		t.Fatalf("RemoveTag: %v", err)
	}
	if err := db.RemoveAlias([]string{jid}); err != nil {
		t.Fatalf("RemoveAlias: %v", err)
	}
	c, err = db.GetContact(jid)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.Alias != "" {
		t.Fatalf("expected alias removed, got %q", c.Alias)
	}
	if len(c.Tags) != 1 || c.Tags[0] != "friends" {
		t.Fatalf("expected remaining tag friends, got %v", c.Tags)
	}
}

func TestContactSystemNamePrecedenceAndSearch(t *testing.T) {
	db := openTestDB(t)

	jid := "111@s.whatsapp.net"
	if err := db.UpsertContact(jid, "111", "Push", "Full Name", "First", "Biz"); err != nil {
		t.Fatalf("UpsertContact: %v", err)
	}
	if err := db.SetSystemName(jid, "System Alice"); err != nil {
		t.Fatalf("SetSystemName: %v", err)
	}

	c, err := db.GetContact(jid)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if c.Name != "System Alice" || c.SystemName != "System Alice" {
		t.Fatalf("contact = %#v", c)
	}

	found, err := db.SearchContacts("System", 10)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(found) != 1 || found[0].JID != jid {
		t.Fatalf("expected system-name match, got %#v", found)
	}

	if err := db.SetAlias([]string{jid}, "Alias Alice"); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}
	c, err = db.GetContact(jid)
	if err != nil {
		t.Fatalf("GetContact after alias: %v", err)
	}
	if c.Name != "Alias Alice" {
		t.Fatalf("alias should win over system name, got %#v", c)
	}

	count, err := db.CountSystemNames()
	if err != nil {
		t.Fatalf("CountSystemNames: %v", err)
	}
	if count != 1 {
		t.Fatalf("system name count = %d, want 1", count)
	}
	cleared, err := db.ClearAllSystemNames()
	if err != nil {
		t.Fatalf("ClearAllSystemNames: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
}
