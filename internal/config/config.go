package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var validAccountName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func DefaultStoreDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".wacli"
	}
	return filepath.Join(home, ".wacli")
}

// ValidateAccountName checks that name is safe for use as a directory name.
func ValidateAccountName(name string) error {
	if name == "" {
		return fmt.Errorf("account name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("account name too long (max 64 characters)")
	}
	if !validAccountName.MatchString(name) {
		return fmt.Errorf("invalid account name %q: must start with alphanumeric and contain only [a-zA-Z0-9._-]", name)
	}
	return nil
}

// AccountDir returns the store directory for a named account.
func AccountDir(baseDir, account string) string {
	return filepath.Join(baseDir, "accounts", account)
}

// ResolveAccount determines which account to use based on flag, env var, or default file.
func ResolveAccount(baseDir, flagValue string) (string, error) {
	if flagValue != "" {
		if err := ValidateAccountName(flagValue); err != nil {
			return "", err
		}
		return flagValue, nil
	}

	if env := os.Getenv("WACLI_ACCOUNT"); env != "" {
		if err := ValidateAccountName(env); err != nil {
			return "", fmt.Errorf("WACLI_ACCOUNT: %w", err)
		}
		return env, nil
	}

	return ReadDefaultAccount(baseDir), nil
}

// ReadDefaultAccount reads the default account name from the base dir.
// Returns "default" if not set.
func ReadDefaultAccount(baseDir string) string {
	b, err := os.ReadFile(filepath.Join(baseDir, "default_account"))
	if err != nil {
		return "default"
	}
	name := strings.TrimSpace(string(b))
	if name == "" {
		return "default"
	}
	return name
}

// WriteDefaultAccount persists the default account name.
func WriteDefaultAccount(baseDir, name string) error {
	if err := ValidateAccountName(name); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(baseDir, "default_account"), []byte(name+"\n"), 0600)
}

// ListAccounts returns all account names found in baseDir/accounts/.
func ListAccounts(baseDir string) ([]string, error) {
	accountsDir := filepath.Join(baseDir, "accounts")
	entries, err := os.ReadDir(accountsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && ValidateAccountName(e.Name()) == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// MaybeMigrateLegacyStore moves a legacy single-account store layout into
// accounts/default/ so existing users get a seamless upgrade.
func MaybeMigrateLegacyStore(baseDir string) error {
	sessionDB := filepath.Join(baseDir, "session.db")
	if _, err := os.Stat(sessionDB); err != nil {
		return nil // no legacy layout
	}

	// Already migrated?
	accountsDir := filepath.Join(baseDir, "accounts")
	if _, err := os.Stat(accountsDir); err == nil {
		return nil // accounts dir already exists, don't touch
	}

	targetDir := AccountDir(baseDir, "default")
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return fmt.Errorf("migrate legacy store: create dir: %w", err)
	}

	filesToMove := []string{"session.db", "session.db-wal", "session.db-shm", "wacli.db", "wacli.db-wal", "wacli.db-shm"}
	for _, name := range filesToMove {
		src := filepath.Join(baseDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(targetDir, name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("migrate legacy store: move %s: %w", name, err)
		}
	}

	// Move media dir if present
	mediaSrc := filepath.Join(baseDir, "media")
	if info, err := os.Stat(mediaSrc); err == nil && info.IsDir() {
		mediaDst := filepath.Join(targetDir, "media")
		if err := os.Rename(mediaSrc, mediaDst); err != nil {
			return fmt.Errorf("migrate legacy store: move media: %w", err)
		}
	}

	// Remove old LOCK file (it will be recreated per-account)
	_ = os.Remove(filepath.Join(baseDir, "LOCK"))

	fmt.Fprintf(os.Stderr, "Migrated legacy store to account \"default\".\n")
	return nil
}
