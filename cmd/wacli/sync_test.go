package main

import (
	"strings"
	"testing"
)

func TestSyncCommandExposesWebhookFlags(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	for _, name := range []string{"webhook", "webhook-secret"} {
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
