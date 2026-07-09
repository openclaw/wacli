package main

import (
	"strings"
	"testing"
)

func TestSyncCommandExposesWebhookFlags(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	for _, name := range []string{"webhook", "webhook-secret", "webhook-allow-private"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
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
