package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultStoreDir(t *testing.T) {
	t.Run("env var overrides default", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "/custom/store/path")
		got := DefaultStoreDir()
		if got != "/custom/store/path" {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, "/custom/store/path")
		}
	})

	t.Run("uses XDG state dir on linux when env unset", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "")
		t.Setenv("XDG_STATE_HOME", "")
		got := DefaultStoreDir()
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".wacli")
		if runtime.GOOS == "linux" {
			want = filepath.Join(home, ".local", "state", "wacli")
		}
		if got != want {
			t.Errorf("DefaultStoreDir() = %q, want %q", got, want)
		}
	})

	t.Run("uses XDG_STATE_HOME on linux", func(t *testing.T) {
		t.Setenv(EnvStoreDir, "")
		t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")

		got := DefaultStoreDir()
		want := filepath.Join("/tmp/xdg-state", "wacli")
		if runtime.GOOS != "linux" {
			home, _ := os.UserHomeDir()
			want = filepath.Join(home, ".wacli")
		}
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
