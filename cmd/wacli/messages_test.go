package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{input: "hello", max: 10, want: "hello"},
		{input: "hello world", max: 5, want: "hell…"},
		{input: "hello", max: 0, want: "hello"},
		{input: "ab", max: 1, want: "a"},
		{input: "hello\nworld", max: 20, want: "hello world"},
		{input: "  hello  ", max: 20, want: "hello"},
	}
	for _, tc := range tests {
		if got := truncate(tc.input, tc.max); got != tc.want {
			t.Fatalf("truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
		}
	}
}

func TestTruncateForDisplay(t *testing.T) {
	const longID = "3EB0B0E8A1B2C3D4E5F6A7B8C9D0"
	if got := tableCell(longID, 14, true); got != longID {
		t.Fatalf("force full = %q, want %q", got, longID)
	}
	if got := fullTableOutputWithTTY(false, false); !got {
		t.Fatalf("non-TTY should request full output")
	}
	if got := tableCell(longID, 14, false); got != "3EB0B0E8A1B2C…" {
		t.Fatalf("tty truncation = %q", got)
	}
}

func TestMessageContextLinePrefersDisplayText(t *testing.T) {
	got := messageContextLine(store.Message{
		Text:        "raw reaction payload",
		DisplayText: "Reacted 👍 to hello",
	})
	if got != "Reacted 👍 to hello" {
		t.Fatalf("messageContextLine() = %q", got)
	}
}

func TestMessageContextLineFallsBackToText(t *testing.T) {
	got := messageContextLine(store.Message{Text: "hello"})
	if got != "hello" {
		t.Fatalf("messageContextLine() = %q", got)
	}
}

func TestMessageContextLineFallsBackToMedia(t *testing.T) {
	got := messageContextLine(store.Message{MediaType: "IMAGE"})
	if got != "Sent image" {
		t.Fatalf("messageContextLine() = %q", got)
	}
}

func TestWriteMessagesListFullOutput(t *testing.T) {
	msg := store.Message{
		ChatJID:     "chat@s.whatsapp.net",
		SenderJID:   "sender@s.whatsapp.net",
		MsgID:       "3EB0B0E8A1B2C3D4E5F6A7B8C9D0",
		Timestamp:   time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		DisplayText: "Reacted 👍 to hello",
		Text:        "raw",
	}

	var truncated bytes.Buffer
	if err := writeMessagesList(&truncated, []store.Message{msg}, false); err != nil {
		t.Fatalf("writeMessagesList truncated: %v", err)
	}
	if strings.Contains(truncated.String(), msg.MsgID) {
		t.Fatalf("expected truncated ID, got output:\n%s", truncated.String())
	}

	var full bytes.Buffer
	if err := writeMessagesList(&full, []store.Message{msg}, true); err != nil {
		t.Fatalf("writeMessagesList full: %v", err)
	}
	if !strings.Contains(full.String(), msg.MsgID) {
		t.Fatalf("expected full ID, got output:\n%s", full.String())
	}
	if !strings.Contains(full.String(), "Reacted 👍 to hello") {
		t.Fatalf("expected display text, got output:\n%s", full.String())
	}
}

func TestMessagesSearchCommandExposesMediaFilters(t *testing.T) {
	cmd := newMessagesSearchCmd(&rootFlags{})
	for _, name := range []string{"has-media", "type"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("expected --%s flag", name)
		}
	}
	if got := cmd.Flags().Lookup("type").Usage; !strings.Contains(got, "text|image|video|audio|document") {
		t.Fatalf("type usage = %q", got)
	}
}
