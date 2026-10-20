//go:build !windows

package delegate

import (
	"os"
	"strings"
	"testing"
)

func TestRefuseRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := socketPath(dir)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ln, err := Listen(dir); err == nil || !strings.Contains(err.Error(), "not a socket") {
		if ln != nil {
			_ = ln.Close()
		}
		t.Fatalf("error = %v, want not a socket", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing file = %q, err = %v", got, err)
	}
}
