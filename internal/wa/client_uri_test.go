package wa

import (
	"strings"
	"testing"
)

// TestNewRejectsURISpecialCharsInStorePath verifies that a StorePath
// containing '?' or '#' is rejected before it can reach the SQLite URI
// parser inside whatsmeow's sqlstore (#177, mirror of #59).
func TestNewRejectsURISpecialCharsInStorePath(t *testing.T) {
	cases := []struct{ name, path string }{
		{"question mark", "/tmp/session.db?mode=memory"},
		{"hash", "/tmp/session.db#frag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Options{StorePath: tc.path})
			if err == nil {
				t.Fatalf("expected error for path %q, got nil", tc.path)
			}
			if !strings.Contains(err.Error(), "'?'") && !strings.Contains(err.Error(), "'#'") {
				t.Fatalf("expected guard error mentioning '?' or '#', got: %v", err)
			}
		})
	}
}
