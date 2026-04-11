package config

import "testing"

func TestResolveStoreDirPrefersFlag(t *testing.T) {
	t.Setenv(StoreDirEnvVar, "/tmp/from-env")

	got := ResolveStoreDir("/tmp/from-flag")
	if got != "/tmp/from-flag" {
		t.Fatalf("ResolveStoreDir() = %q, want explicit flag value", got)
	}
}

func TestResolveStoreDirUsesEnv(t *testing.T) {
	t.Setenv(StoreDirEnvVar, "/tmp/from-env")

	got := ResolveStoreDir("")
	if got != "/tmp/from-env" {
		t.Fatalf("ResolveStoreDir() = %q, want env value", got)
	}
}

func TestResolveStoreDirIgnoresWhitespaceEnv(t *testing.T) {
	t.Setenv(StoreDirEnvVar, "   ")

	got := ResolveStoreDir("")
	if got != DefaultStoreDir() {
		t.Fatalf("ResolveStoreDir() = %q, want default store dir %q", got, DefaultStoreDir())
	}
}
