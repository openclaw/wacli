package pathutil

import "testing"

func TestSanitizeSegment(t *testing.T) {
	if got := SanitizeSegment(""); got != "unknown" {
		t.Fatalf("expected unknown, got %q", got)
	}
	if got := SanitizeSegment(" ../a/b:c@d "); got == "" || got == " ../a/b:c@d " {
		t.Fatalf("unexpected sanitize result: %q", got)
	}
	if got := SanitizeSegment("a/b"); got != "a_b" {
		t.Fatalf("expected a_b, got %q", got)
	}
}

func TestSanitizeFilename(t *testing.T) {
	if got := SanitizeFilename(""); got != "file" {
		t.Fatalf("expected file, got %q", got)
	}
	if got := SanitizeFilename(".."); got == ".." {
		t.Fatalf("expected .. to be sanitized, got %q", got)
	}
	if got := SanitizeFilename("a/b"); got != "a_b" {
		t.Fatalf("expected a_b, got %q", got)
	}
}

// TestSanitizeControlChars verifies that null bytes and control characters
// are stripped from both SanitizeSegment and SanitizeFilename (#60).
func TestSanitizeControlChars(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"null byte", "foo\x00bar"},
		{"null byte only", "\x00"},
		{"tab", "foo\tbar"},
		{"newline", "foo\nbar"},
		{"carriage return", "foo\rbar"},
		{"bell", "foo\x07bar"},
		{"multiple control", "\x00\x01\x02hello\x7f"},
	}

	for _, tc := range cases {
		t.Run("SanitizeSegment/"+tc.name, func(t *testing.T) {
			got := SanitizeSegment(tc.input)
			for _, r := range got {
				if r == 0 || (r < 0x20 && r != 0) || r == 0x7f {
					t.Errorf("SanitizeSegment(%q) = %q, still contains control char %q", tc.input, got, r)
				}
			}
		})
		t.Run("SanitizeFilename/"+tc.name, func(t *testing.T) {
			got := SanitizeFilename(tc.input)
			for _, r := range got {
				if r == 0 || (r < 0x20 && r != 0) || r == 0x7f {
					t.Errorf("SanitizeFilename(%q) = %q, still contains control char %q", tc.input, got, r)
				}
			}
		})
	}

	// A segment of only control chars should fall back to "unknown" — after
	// stripping, the string becomes empty and TrimSpace+fallback applies.
	if got := SanitizeSegment("\x00\x01"); got != "unknown" {
		t.Errorf("SanitizeSegment of only control chars: want \"unknown\", got %q", got)
	}
}
