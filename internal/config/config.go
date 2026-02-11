package config

import (
	"os"
	"path/filepath"
)

func DefaultStoreDir() string {
	if dir := os.Getenv("WACLI_STORE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".wacli"
	}
	return filepath.Join(home, ".wacli")
}
