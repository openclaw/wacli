package config

import (
	"os"
	"path/filepath"
)

// EnvStoreDir is the environment variable that overrides the default store
// directory. This is useful for Docker, CI, and multi-tenant deployments
// where the store path needs to be configured without passing --store on
// every invocation.
const EnvStoreDir = "WACLI_STORE_DIR"

// DefaultStoreDir returns the store directory to use when --store is not
// supplied. It checks WACLI_STORE_DIR first, then falls back to ~/.wacli.
func DefaultStoreDir() string {
	if dir := os.Getenv(EnvStoreDir); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".wacli"
	}
	return filepath.Join(home, ".wacli")
}
