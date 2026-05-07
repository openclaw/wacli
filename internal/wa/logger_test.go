package wa

import (
	"bytes"
	"strings"
	"testing"
)

func TestWhatsmeowLoggerWritesToConfiguredWriter(t *testing.T) {
	var stderr bytes.Buffer
	logger := newWhatsmeowLogger("Client", "ERROR", &stderr)

	logger.Warnf("hidden warning")
	if stderr.Len() != 0 {
		t.Fatalf("WARN was written for ERROR logger: %q", stderr.String())
	}

	logger.Errorf("Failed to issue privacy token for %s: %v", "123@s.whatsapp.net", "bad-request")
	got := stderr.String()
	if !strings.Contains(got, "[Client ERROR] Failed to issue privacy token for 123@s.whatsapp.net: bad-request") {
		t.Fatalf("unexpected log line: %q", got)
	}
}

func TestWhatsmeowLoggerSubmoduleSharesWriter(t *testing.T) {
	var stderr bytes.Buffer
	logger := newWhatsmeowLogger("Client", "ERROR", &stderr)

	logger.Sub("Socket").Errorf("boom")
	got := stderr.String()
	if !strings.Contains(got, "[Client/Socket ERROR] boom") {
		t.Fatalf("unexpected submodule log line: %q", got)
	}
}
