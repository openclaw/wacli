package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateDirCreatesAndChmods(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory")
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %04o, want 0700", got)
	}
}

func TestEnsurePrivateDirFixesExistingPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod setup: %v", err)
	}
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %04o, want 0700", got)
	}
}

func TestEnsurePrivateDirRejectsFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := WritePrivateFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := EnsurePrivateDir(path); err == nil {
		t.Fatalf("expected error for file path")
	}
}

func TestWritePrivateFileCreatesOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := WritePrivateFile(path, []byte("secret")); err != nil {
		t.Fatalf("WritePrivateFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "secret" {
		t.Fatalf("data = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

func TestWritePrivateFileNarrowsExistingPermsBeforeRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.Write([]byte("old-secret")); err != nil {
		_ = f.Close()
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := WritePrivateFile(path, []byte("new-secret")); err != nil {
		t.Fatalf("WritePrivateFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "new-secret" {
		t.Fatalf("data = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

func TestWritePrivateFileAtomicReplacesOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := WritePrivateFileAtomic(path, []byte("old-secret")); err != nil {
		t.Fatalf("WritePrivateFileAtomic old: %v", err)
	}
	if err := WritePrivateFileAtomic(path, []byte("new-secret")); err != nil {
		t.Fatalf("WritePrivateFileAtomic new: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "new-secret" {
		t.Fatalf("data = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".file.tmp-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("left temp files: %v", matches)
	}
}

func TestEnsureWritableDirDoesNotChmodExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod setup: %v", err)
	}
	if err := EnsureWritableDir(dir); err != nil {
		t.Fatalf("EnsureWritableDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode = %04o, want 0755", got)
	}
}

func TestEnsureWritableDirCreatesPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	if err := EnsureWritableDir(dir); err != nil {
		t.Fatalf("EnsureWritableDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %04o, want 0700", got)
	}
}

func TestEnsureWritableDirRejectsNonWritableDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod setup: %v", err)
	}
	defer os.Chmod(dir, 0o700)

	if err := EnsureWritableDir(dir); err == nil {
		t.Fatalf("expected error for non-writable dir")
	}
}
