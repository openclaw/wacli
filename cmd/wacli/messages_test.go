package main

import (
	"testing"

	"github.com/steipete/wacli/internal/store"
)

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
