package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/fsutil"
)

func TestDefaultStoreDir(t *testing.T) {
	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "/custom/store/path")
		got := DefaultStoreDir()
		if got != "/custom/store/path" {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, "/custom/store/path")
		}
	})

	t.Run("falls back to platform default when env unset", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "")
		t.Setenv("XDG_STATE_HOME", "")
		got := DefaultStoreDir()
		home, _ := os.UserHomeDir()
		want := defaultStoreDirFor(runtime.GOOS, home, "", pathExists)
		if got != want {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, want)
		}
	})

	t.Run("env var constant is WACLI_STORE_DIR", func(t *testing.T) {
		if EnvStoreDir != "WACLI_STORE_DIR" {
			t.Errorf("EnvStoreDir = %q, want %q", EnvStoreDir, "WACLI_STORE_DIR")
		}
	})
}

func TestDefaultStoreDirFor(t *testing.T) {
	t.Run("uses XDG_STATE_HOME on linux", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "/state", func(string) bool { return false })
		want := filepath.Join("/state", "wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("uses XDG default on linux", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "", func(string) bool { return false })
		want := filepath.Join("/home/alice", ".local", "state", "wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("keeps existing legacy linux store", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "", func(path string) bool {
			return path == filepath.Join("/home/alice", ".wacli")
		})
		want := filepath.Join("/home/alice", ".wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("uses XDG when both linux stores exist", func(t *testing.T) {
		got := defaultStoreDirFor("linux", "/home/alice", "", func(string) bool { return true })
		want := filepath.Join("/home/alice", ".local", "state", "wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})

	t.Run("keeps home dotdir outside linux", func(t *testing.T) {
		got := defaultStoreDirFor("darwin", "/Users/alice", "/state", func(string) bool { return false })
		want := filepath.Join("/Users/alice", ".wacli")
		if got != want {
			t.Fatalf("defaultStoreDirFor = %q, want %q", got, want)
		}
	})
}

func TestAccountsConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &AccountsConfig{
		DefaultAccount: "personal",
		Accounts: map[string]AccountEntry{
			"personal": {Store: "accounts/personal"},
			"work":     {Store: "/tmp/wacli-work", Label: "Work"},
		},
	}

	if err := SaveAccountsConfig(path, cfg); err != nil {
		t.Fatalf("SaveAccountsConfig: %v", err)
	}
	loaded, err := LoadAccountsConfig(path)
	if err != nil {
		t.Fatalf("LoadAccountsConfig: %v", err)
	}
	if loaded.DefaultAccount != "personal" {
		t.Fatalf("DefaultAccount = %q, want personal", loaded.DefaultAccount)
	}
	store, account, err := ResolveAccountStore(path, "personal")
	if err != nil {
		t.Fatalf("ResolveAccountStore: %v", err)
	}
	wantStore := filepath.Join(filepath.Dir(path), "accounts", "personal")
	if store != wantStore || account.StoreDir != wantStore {
		t.Fatalf("store = %q/%q, want %q", store, account.StoreDir, wantStore)
	}
	if !account.Default {
		t.Fatal("account.Default = false, want true")
	}
}

func TestAccountsConfigKnownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := fsutil.WritePrivateFile(path, []byte("unknown: true\n")); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAccountsConfig(path)
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("LoadAccountsConfig error = %v, want unknown field error", err)
	}
}

func TestValidateAccountName(t *testing.T) {
	valid := []string{"personal", "work-2", "client.foo", "a_b"}
	for _, name := range valid {
		if err := ValidateAccountName(name); err != nil {
			t.Fatalf("ValidateAccountName(%q): %v", name, err)
		}
	}

	invalid := []string{"", " work", "work ", ".hidden", "-work", "bad/name", "bad?name", "bad name"}
	for _, name := range invalid {
		if err := ValidateAccountName(name); err == nil {
			t.Fatalf("ValidateAccountName(%q) succeeded, want error", name)
		}
	}
}
