package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAccountName(t *testing.T) {
	valid := []string{"default", "work", "personal", "my-account", "account.2", "Account_1"}
	for _, name := range valid {
		if err := ValidateAccountName(name); err != nil {
			t.Errorf("ValidateAccountName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{"", "-starts-with-dash", ".dot", "../traversal", "has space", "has/slash", "a\x00b"}
	for _, name := range invalid {
		if err := ValidateAccountName(name); err == nil {
			t.Errorf("ValidateAccountName(%q) = nil, want error", name)
		}
	}

	// 65 chars
	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := ValidateAccountName(long); err == nil {
		t.Error("ValidateAccountName(65 chars) = nil, want error")
	}
}

func TestResolveAccount(t *testing.T) {
	dir := t.TempDir()

	// No flag, no env, no file → "default"
	name, err := ResolveAccount(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "default" {
		t.Errorf("got %q, want %q", name, "default")
	}

	// Write default_account file
	if err := WriteDefaultAccount(dir, "work"); err != nil {
		t.Fatal(err)
	}
	name, err = ResolveAccount(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "work" {
		t.Errorf("got %q, want %q", name, "work")
	}

	// Flag overrides file
	name, err = ResolveAccount(dir, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if name != "personal" {
		t.Errorf("got %q, want %q", name, "personal")
	}

	// Env overrides file (when no flag)
	t.Setenv("WACLI_ACCOUNT", "env-acct")
	name, err = ResolveAccount(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "env-acct" {
		t.Errorf("got %q, want %q", name, "env-acct")
	}
}

func TestListAccounts(t *testing.T) {
	dir := t.TempDir()

	// No accounts dir → empty
	accounts, err := ListAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Errorf("got %v, want empty", accounts)
	}

	// Create some accounts
	for _, name := range []string{"default", "work", "personal"} {
		if err := os.MkdirAll(filepath.Join(dir, "accounts", name), 0700); err != nil {
			t.Fatal(err)
		}
	}

	accounts, err = ListAccounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 3 {
		t.Errorf("got %d accounts, want 3", len(accounts))
	}
}

func TestMaybeMigrateLegacyStore(t *testing.T) {
	dir := t.TempDir()

	// Create legacy layout
	os.WriteFile(filepath.Join(dir, "session.db"), []byte("session"), 0600)
	os.WriteFile(filepath.Join(dir, "wacli.db"), []byte("index"), 0600)
	os.MkdirAll(filepath.Join(dir, "media"), 0700)
	os.WriteFile(filepath.Join(dir, "media", "test.jpg"), []byte("img"), 0600)

	if err := MaybeMigrateLegacyStore(dir); err != nil {
		t.Fatal(err)
	}

	// Verify files moved
	target := AccountDir(dir, "default")
	if b, err := os.ReadFile(filepath.Join(target, "session.db")); err != nil || string(b) != "session" {
		t.Error("session.db not migrated correctly")
	}
	if b, err := os.ReadFile(filepath.Join(target, "wacli.db")); err != nil || string(b) != "index" {
		t.Error("wacli.db not migrated correctly")
	}
	if b, err := os.ReadFile(filepath.Join(target, "media", "test.jpg")); err != nil || string(b) != "img" {
		t.Error("media not migrated correctly")
	}

	// Old files should be gone
	if _, err := os.Stat(filepath.Join(dir, "session.db")); !os.IsNotExist(err) {
		t.Error("old session.db still exists")
	}

	// Second call should be a no-op
	if err := MaybeMigrateLegacyStore(dir); err != nil {
		t.Fatal("second migration should be no-op:", err)
	}
}
