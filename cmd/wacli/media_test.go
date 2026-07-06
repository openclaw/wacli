package main

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestMediaBulkTimeoutIsOptIn(t *testing.T) {
	flags := &rootFlags{timeout: 5 * time.Minute}
	root := &cobra.Command{Use: "wacli"}
	root.PersistentFlags().DurationVar(&flags.timeout, "timeout", flags.timeout, "command timeout")
	cmd := newMediaBackfillCmd(flags)
	root.AddCommand(cmd)

	if mediaBulkTimeoutEnabled(cmd, flags) {
		t.Fatalf("default timeout unexpectedly enabled")
	}
	if err := root.PersistentFlags().Set("timeout", "1s"); err != nil {
		t.Fatalf("set timeout: %v", err)
	}
	if !mediaBulkTimeoutEnabled(cmd, flags) {
		t.Fatalf("explicit timeout was not enabled")
	}
}

func TestParseMediaRetryBefore(t *testing.T) {
	got, err := parseMediaRetryBefore(" 2026-01-02 ")
	if err != nil {
		t.Fatalf("parseMediaRetryBefore: %v", err)
	}
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).Unix()
	if got != want {
		t.Fatalf("timestamp = %d, want %d", got, want)
	}
	if _, err := parseMediaRetryBefore("02-01-2026"); err == nil {
		t.Fatalf("expected invalid date error")
	}
}

func TestMediaRetryRejectsInvalidOptionsBeforeStoreAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "limit", args: []string{"--limit", "-1"}, want: "--limit must be >= 0"},
		{name: "batch", args: []string{"--batch", "0"}, want: "--batch must be > 0"},
		{name: "wait", args: []string{"--wait", "0"}, want: "--wait must be > 0"},
		{name: "before", args: []string{"--before", "01-02-2026"}, want: "--before must be YYYY-MM-DD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--account", "does-not-exist", "media", "retry"}, tc.args...)
			err := execute(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("execute error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMediaDownloadReadOnlyRequiresOutput(t *testing.T) {
	err := execute([]string{"--read-only", "media", "download", "--chat", "chat@s.whatsapp.net", "--id", "mid"})
	if err == nil || !strings.Contains(err.Error(), "--output is required in read-only mode") {
		t.Fatalf("execute error = %v, want read-only output requirement", err)
	}
}
