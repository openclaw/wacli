package sqliteutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChmodFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	for _, p := range []string{path, path + "-wal"} {
		if err := writeTestFileMode(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
	}

	if err := ChmodFiles(path, 0o600); err != nil {
		t.Fatalf("ChmodFiles: %v", err)
	}

	for _, p := range []string{path, path + "-wal"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", filepath.Base(p), got)
		}
	}
}

func TestChmodFilesIgnoresMissingSidecars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	if err := writeTestFileMode(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := ChmodFiles(path, 0o600); err != nil {
		t.Fatalf("ChmodFiles: %v", err)
	}
}

func TestFileURIEscapesPathDelimiters(t *testing.T) {
	uri := FileURI(filepath.Join(t.TempDir(), "store%3fprod", "session?prod#1.db"), "_foreign_keys=on")
	if filepath.Base(uri) == "session?prod#1.db?_foreign_keys=on" {
		t.Fatalf("FileURI = %q, want escaped URI delimiters", uri)
	}
	if !strings.Contains(uri, "store%253fprod/session%3Fprod%231.db?_foreign_keys=on") {
		t.Fatalf("FileURI = %q, want escaped path with raw query", uri)
	}
}

func TestFileURIWindowsPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "drive letter",
			path: `C:\Users\me\My Store\wacli.db`,
			want: "file:///C:/Users/me/My%20Store/wacli.db?_foreign_keys=on",
		},
		{
			name: "UNC share",
			path: `\\server\share\My Store\wacli.db`,
			want: "file:////server/share/My%20Store/wacli.db?_foreign_keys=on",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FileURI(tt.path, "_foreign_keys=on"); got != tt.want {
				t.Fatalf("FileURI(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func writeTestFileMode(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
