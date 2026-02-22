package main

import (
	"testing"

	"github.com/steipete/wacli/internal/store"
)

func TestSenderLabel(t *testing.T) {
	t.Run("from_me_always_me", func(t *testing.T) {
		m := store.Message{
			FromMe:     true,
			SenderJID:  "sender@s.whatsapp.net",
			SenderName: "Alice",
		}
		if got := senderLabel(m, false); got != "me" {
			t.Fatalf("expected me, got %q", got)
		}
		if got := senderLabel(m, true); got != "me" {
			t.Fatalf("expected me, got %q", got)
		}
	})

	t.Run("show_names_uses_sender_name_when_present", func(t *testing.T) {
		m := store.Message{
			SenderJID:  "sender@s.whatsapp.net",
			SenderName: "Alice",
		}
		if got := senderLabel(m, true); got != "Alice" {
			t.Fatalf("expected Alice, got %q", got)
		}
	})

	t.Run("falls_back_to_jid", func(t *testing.T) {
		m := store.Message{
			SenderJID: "sender@s.whatsapp.net",
		}
		if got := senderLabel(m, true); got != "sender@s.whatsapp.net" {
			t.Fatalf("expected sender@s.whatsapp.net, got %q", got)
		}
		if got := senderLabel(m, false); got != "sender@s.whatsapp.net" {
			t.Fatalf("expected sender@s.whatsapp.net, got %q", got)
		}
	})
}
