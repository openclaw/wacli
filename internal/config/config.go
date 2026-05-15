package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/openclaw/wacli/internal/fsutil"
	"gopkg.in/yaml.v3"
)

// EnvStoreDir is the environment variable that overrides the default store
// directory. This is useful for Docker, CI, and multi-tenant deployments
// where the store path needs to be configured without passing --store on
// every invocation.
const EnvStoreDir = "WACLI_STORE_DIR"

const ConfigFileName = "config.yaml"

var accountNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type AccountsConfig struct {
	DefaultAccount string                  `yaml:"default_account,omitempty"`
	Accounts       map[string]AccountEntry `yaml:"accounts,omitempty"`
}

type AccountEntry struct {
	Store string `yaml:"store"`
	Label string `yaml:"label,omitempty"`
}

type Account struct {
	Name            string
	Label           string
	ConfiguredStore string
	StoreDir        string
	Default         bool
}

// DefaultStoreDir returns the store directory to use when --store is not
// supplied. It checks WACLI_STORE_DIR first, then falls back to the XDG state
// directory on Linux or ~/.wacli on other platforms.
func DefaultStoreDir() string {
	if dir := os.Getenv(EnvStoreDir); dir != "" {
		return dir
	}
	return DefaultBaseDir()
}

// DefaultBaseDir returns wacli's platform default state root without honoring
// WACLI_STORE_DIR. Account config lives here so a temporary store override
// does not hide the account registry.
func DefaultBaseDir() string {
	xdgStateHome := os.Getenv("XDG_STATE_HOME")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if runtime.GOOS == "linux" && xdgStateHome != "" {
			return filepath.Join(xdgStateHome, "wacli")
		}
		return ".wacli"
	}
	return defaultStoreDirFor(runtime.GOOS, home, xdgStateHome, pathExists)
}

func DefaultConfigPath() string {
	return filepath.Join(DefaultBaseDir(), ConfigFileName)
}

func ValidateAccountName(name string) error {
	if name == "" {
		return fmt.Errorf("account name is required")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid account name %q: leading or trailing whitespace is not allowed", name)
	}
	if !accountNameRE.MatchString(name) {
		return fmt.Errorf("invalid account name %q: use letters, digits, '.', '_', or '-', starting with a letter or digit", name)
	}
	return nil
}

func LoadAccountsConfig(path string) (*AccountsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg AccountsConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			cfg = AccountsConfig{}
		} else {
			return nil, fmt.Errorf("parse account config %s: %w", path, err)
		}
	}
	if cfg.Accounts == nil {
		cfg.Accounts = map[string]AccountEntry{}
	}
	for name, entry := range cfg.Accounts {
		if err := ValidateAccountName(name); err != nil {
			return nil, err
		}
		if strings.TrimSpace(entry.Store) == "" {
			return nil, fmt.Errorf("account %q store is required", name)
		}
		if strings.ContainsAny(entry.Store, "?#") {
			return nil, fmt.Errorf("account %q store must not contain '?' or '#'", name)
		}
	}
	if cfg.DefaultAccount != "" {
		if err := ValidateAccountName(cfg.DefaultAccount); err != nil {
			return nil, fmt.Errorf("default_account: %w", err)
		}
		if _, ok := cfg.Accounts[cfg.DefaultAccount]; !ok {
			return nil, fmt.Errorf("default_account %q is not defined", cfg.DefaultAccount)
		}
	}
	return &cfg, nil
}

func LoadAccountsConfigIfExists(path string) (*AccountsConfig, bool, error) {
	cfg, err := LoadAccountsConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &AccountsConfig{Accounts: map[string]AccountEntry{}}, false, nil
		}
		return nil, false, err
	}
	return cfg, true, nil
}

func SaveAccountsConfig(path string, cfg *AccountsConfig) error {
	if cfg == nil {
		cfg = &AccountsConfig{}
	}
	if cfg.Accounts == nil {
		cfg.Accounts = map[string]AccountEntry{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode account config: %w", err)
	}
	tmp := path + ".tmp"
	if err := fsutil.WritePrivateFile(tmp, data); err != nil {
		return fmt.Errorf("write account config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace account config: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func ResolveAccountStore(path, name string) (string, Account, error) {
	if err := ValidateAccountName(name); err != nil {
		return "", Account{}, err
	}
	cfg, err := LoadAccountsConfig(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", Account{}, fmt.Errorf("account config not found at %s; run `wacli accounts add %s`", path, name)
		}
		return "", Account{}, err
	}
	entry, ok := cfg.Accounts[name]
	if !ok {
		return "", Account{}, fmt.Errorf("account %q is not configured; run `wacli accounts list`", name)
	}
	storeDir := resolveConfiguredStore(filepath.Dir(path), entry.Store)
	return storeDir, Account{
		Name:            name,
		Label:           entry.Label,
		ConfiguredStore: entry.Store,
		StoreDir:        storeDir,
		Default:         cfg.DefaultAccount == name,
	}, nil
}

func ListAccounts(path string, cfg *AccountsConfig) []Account {
	if cfg == nil || len(cfg.Accounts) == 0 {
		return nil
	}
	out := make([]Account, 0, len(cfg.Accounts))
	for name, entry := range cfg.Accounts {
		out = append(out, Account{
			Name:            name,
			Label:           entry.Label,
			ConfiguredStore: entry.Store,
			StoreDir:        resolveConfiguredStore(filepath.Dir(path), entry.Store),
			Default:         cfg.DefaultAccount == name,
		})
	}
	return out
}

func DefaultAccountStore(name string) string {
	return filepath.ToSlash(filepath.Join("accounts", name))
}

func resolveConfiguredStore(baseDir, store string) string {
	if filepath.IsAbs(store) {
		return filepath.Clean(store)
	}
	return filepath.Clean(filepath.Join(baseDir, store))
}

func defaultStoreDirFor(goos, home, xdgStateHome string, exists func(string) bool) string {
	legacy := filepath.Join(home, ".wacli")
	if goos != "linux" {
		return legacy
	}
	if xdgStateHome != "" {
		return filepath.Join(xdgStateHome, "wacli")
	}
	xdgDefault := filepath.Join(home, ".local", "state", "wacli")
	if exists(legacy) && !exists(xdgDefault) {
		return legacy
	}
	return xdgDefault
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
