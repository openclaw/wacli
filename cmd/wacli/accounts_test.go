package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/config"
)

func TestAccountsAddNoAuthCreatesConfig(t *testing.T) {
	isolateAccountConfigHome(t)

	var stdout string
	stderr := captureRootStderr(t, func() {
		stdout = captureRootStdout(t, func() {
			if err := execute([]string{"accounts", "add", "personal", "--no-auth"}); err != nil {
				t.Fatalf("execute accounts add: %v", err)
			}
		})
	})
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Account personal added") {
		t.Fatalf("stdout = %q, want account added", stdout)
	}

	cfg, err := config.LoadAccountsConfig(config.DefaultConfigPath())
	if err != nil {
		t.Fatalf("LoadAccountsConfig: %v", err)
	}
	if cfg.DefaultAccount != "personal" {
		t.Fatalf("DefaultAccount = %q, want personal", cfg.DefaultAccount)
	}
	account, ok := cfg.Accounts["personal"]
	if !ok {
		t.Fatal("personal account missing")
	}
	if account.Store != "accounts/personal" {
		t.Fatalf("Store = %q, want accounts/personal", account.Store)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config.DefaultConfigPath()), "accounts", "personal")); err != nil {
		t.Fatalf("account store not created: %v", err)
	}
}

func TestAccountsAddValidatesAuthFlagsBeforeSaving(t *testing.T) {
	isolateAccountConfigHome(t)

	err := execute([]string{"--json", "accounts", "add", "personal", "--qr-format", "text"})
	if err == nil || !strings.Contains(err.Error(), "--qr-format=text cannot be combined with --json") {
		t.Fatalf("execute error = %v, want QR/json validation error", err)
	}
	if _, statErr := os.Stat(config.DefaultConfigPath()); !os.IsNotExist(statErr) {
		t.Fatalf("config stat error = %v, want not exist", statErr)
	}
	storeDir := filepath.Join(filepath.Dir(config.DefaultConfigPath()), "accounts", "personal")
	if _, statErr := os.Stat(storeDir); !os.IsNotExist(statErr) {
		t.Fatalf("store stat error = %v, want not exist", statErr)
	}
}

func TestAccountsAddRejectsWhitespaceName(t *testing.T) {
	isolateAccountConfigHome(t)

	err := execute([]string{"accounts", "add", " work ", "--no-auth"})
	if err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("execute error = %v, want whitespace validation error", err)
	}
	if _, statErr := os.Stat(config.DefaultConfigPath()); !os.IsNotExist(statErr) {
		t.Fatalf("config stat error = %v, want not exist", statErr)
	}
}

func TestAccountsListJSON(t *testing.T) {
	isolateAccountConfigHome(t)
	cfgPath := config.DefaultConfigPath()
	cfg := &config.AccountsConfig{
		DefaultAccount: "work",
		Accounts: map[string]config.AccountEntry{
			"personal": {Store: "accounts/personal"},
			"work":     {Store: "accounts/work"},
		},
	}
	if err := config.SaveAccountsConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout string
	stdout = captureRootStdout(t, func() {
		if err := execute([]string{"--json", "accounts", "list"}); err != nil {
			t.Fatalf("execute accounts list: %v", err)
		}
	})

	var payload struct {
		Data struct {
			DefaultAccount string `json:"default_account"`
			Accounts       []struct {
				Name    string `json:"name"`
				Default bool   `json:"default"`
			} `json:"accounts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", stdout, err)
	}
	if payload.Data.DefaultAccount != "work" || len(payload.Data.Accounts) != 2 {
		t.Fatalf("payload = %+v, want work and 2 accounts", payload)
	}
	if payload.Data.Accounts[0].Name != "personal" || payload.Data.Accounts[1].Name != "work" || !payload.Data.Accounts[1].Default {
		t.Fatalf("accounts = %+v, want sorted personal/work with work default", payload.Data.Accounts)
	}
}
