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

func TestSanitizeStripsNullByte(t *testing.T) {
	if got := SanitizeSegment("foo" + string(rune(0)) + "bar"); got != "foobar" {
		t.Fatalf("expected foobar, got %q", got)
	}
}

func TestSanitizeStripsTab(t *testing.T) {
	if got := SanitizeSegment("hello\tworld"); got != "helloworld" {
		t.Fatalf("expected helloworld, got %q", got)
	}
}

func TestSanitizeStripsControlChars(t *testing.T) {
	input := "a" + string(rune(1)) + "b" + string(rune(31)) + "c" + string(rune(127)) + "d"
	if got := SanitizeSegment(input); got != "abcd" {
		t.Fatalf("expected abcd, got %q", got)
	}
}

func TestSanitizeFilenameStripsControlChars(t *testing.T) {
	if got := SanitizeFilename("file" + string(rune(0)) + "name"); got != "filename" {
		t.Fatalf("expected filename, got %q", got)
	}
}
