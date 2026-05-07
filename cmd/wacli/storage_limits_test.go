package main

import "testing"

func TestParseByteSize(t *testing.T) {
	tests := map[string]int64{
		"":      0,
		"0":     0,
		"512":   512,
		"1kb":   1024,
		"2 MB":  2 * 1024 * 1024,
		"1.5GB": int64(1.5 * 1024 * 1024 * 1024),
	}
	for raw, want := range tests {
		got, err := parseByteSize(raw)
		if err != nil {
			t.Fatalf("parseByteSize(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("parseByteSize(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestParseByteSizeRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"abc", "-1", "1XB"} {
		if _, err := parseByteSize(raw); err == nil {
			t.Fatalf("parseByteSize(%q) expected error", raw)
		}
	}
}

func TestResolveSyncStorageLimitsReadsEnv(t *testing.T) {
	t.Setenv(envSyncMaxMessages, "123")
	t.Setenv(envSyncMaxDBSize, "2MB")

	maxMessages, maxDBSize, err := resolveSyncStorageLimits(syncStorageLimitFlags{})
	if err != nil {
		t.Fatalf("resolveSyncStorageLimits: %v", err)
	}
	if maxMessages != 123 {
		t.Fatalf("maxMessages = %d, want 123", maxMessages)
	}
	if maxDBSize != 2*1024*1024 {
		t.Fatalf("maxDBSize = %d, want 2MiB", maxDBSize)
	}
}

func TestResolveSyncStorageLimitsFlagsOverrideEnv(t *testing.T) {
	t.Setenv(envSyncMaxMessages, "123")
	t.Setenv(envSyncMaxDBSize, "2MB")

	maxMessages, maxDBSize, err := resolveSyncStorageLimits(syncStorageLimitFlags{
		maxMessages: 5,
		maxDBSize:   "4MB",
	})
	if err != nil {
		t.Fatalf("resolveSyncStorageLimits: %v", err)
	}
	if maxMessages != 5 {
		t.Fatalf("maxMessages = %d, want 5", maxMessages)
	}
	if maxDBSize != 4*1024*1024 {
		t.Fatalf("maxDBSize = %d, want 4MiB", maxDBSize)
	}
}

func TestResolveSyncStorageLimitsExplicitZeroMaxMessagesOverridesEnv(t *testing.T) {
	t.Setenv(envSyncMaxMessages, "123")

	maxMessages, _, err := resolveSyncStorageLimits(syncStorageLimitFlags{
		maxMessages:    0,
		maxMessagesSet: true,
	})
	if err != nil {
		t.Fatalf("resolveSyncStorageLimits: %v", err)
	}
	if maxMessages != 0 {
		t.Fatalf("maxMessages = %d, want explicit unlimited", maxMessages)
	}
}
