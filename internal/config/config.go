package config

import (
	"os"
	"path/filepath"
	"strings"
)

const StoreDirEnvVar = "WACLI_STORE_DIR"

func DefaultStoreDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".wacli"
	}
	return filepath.Join(home, ".wacli")
}

func ResolveStoreDir(explicit string) string {
	if s := strings.TrimSpace(explicit); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv(StoreDirEnvVar)); s != "" {
		return s
	}
	return DefaultStoreDir()
}
