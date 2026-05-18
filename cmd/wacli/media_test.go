package main

import (
	"strings"
	"testing"
)

func TestMediaDownloadReadOnlyRequiresOutput(t *testing.T) {
	err := execute([]string{"--read-only", "media", "download", "--chat", "chat@s.whatsapp.net", "--id", "mid"})
	if err == nil || !strings.Contains(err.Error(), "--output is required in read-only mode") {
		t.Fatalf("execute error = %v, want read-only output requirement", err)
	}
}
