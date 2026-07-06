package main

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestMediaBackfillTimeoutIsOptIn(t *testing.T) {
	flags := &rootFlags{timeout: 5 * time.Minute}
	root := &cobra.Command{Use: "wacli"}
	root.PersistentFlags().DurationVar(&flags.timeout, "timeout", flags.timeout, "command timeout")
	cmd := newMediaBackfillCmd(flags)
	root.AddCommand(cmd)

	if mediaBackfillTimeoutEnabled(cmd, flags) {
		t.Fatalf("default timeout unexpectedly enabled")
	}
	if err := root.PersistentFlags().Set("timeout", "1s"); err != nil {
		t.Fatalf("set timeout: %v", err)
	}
	if !mediaBackfillTimeoutEnabled(cmd, flags) {
		t.Fatalf("explicit timeout was not enabled")
	}
}

func TestMediaDownloadReadOnlyRequiresOutput(t *testing.T) {
	err := execute([]string{"--read-only", "media", "download", "--chat", "chat@s.whatsapp.net", "--id", "mid"})
	if err == nil || !strings.Contains(err.Error(), "--output is required in read-only mode") {
		t.Fatalf("execute error = %v, want read-only output requirement", err)
	}
}
