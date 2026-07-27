package main

import (
	"strings"
	"testing"
)

func TestSyncCommandExposesWebhookFlags(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	for _, name := range []string{"webhook", "webhook-secret", "webhook-allow-private", "webhook-events"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestSyncCommandWebhookEventsDefaultsToMessage(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	flag := cmd.Flags().Lookup("webhook-events")
	if flag == nil {
		t.Fatal("missing --webhook-events flag")
	}
	if flag.DefValue != "message" {
		t.Fatalf("--webhook-events default = %q, want \"message\"", flag.DefValue)
	}
}

func TestSyncCommandRejectsUnknownWebhookEvent(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook", "https://example.test/hook", "--webhook-events", "message,presence"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-events must be a comma-separated list of") {
		t.Fatalf("expected webhook-events validation error, got %v", err)
	}
}

func TestSyncCommandRequiresWebhookForEvents(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook-events", "receipt"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-events requires --webhook") {
		t.Fatalf("expected webhook-events validation error, got %v", err)
	}
}

func TestSyncCommandRequiresWebhookForSecret(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook-secret", "secret"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-secret requires --webhook") {
		t.Fatalf("expected webhook-secret validation error, got %v", err)
	}
}

func TestSyncCommandRejectsIneffectiveStaleThreshold(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--stale-threshold", "2m20s"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--stale-threshold must be less than 2m20s") {
		t.Fatalf("expected stale-threshold validation error, got %v", err)
	}
}

func TestSyncCommandRejectsInvalidPresenceMode(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--presence-mode", "loud"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--presence-mode must be one of: normal, quiet") {
		t.Fatalf("expected presence-mode validation error, got %v", err)
	}
}
