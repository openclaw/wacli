package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Delegated sends call fetchLinkPreview inside `sync --follow`, whose stderr
// can be an NDJSON event stream, so the helper must not print anything.
func TestFetchLinkPreviewReturnsWarningWithoutWritingStderr(t *testing.T) {
	t.Setenv("WACLI_LINKPREVIEW_ALLOW_CIDRS", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="x"></head></html>`))
	}))
	defer srv.Close()

	var warning string
	stderr := captureRootStderr(t, func() {
		preview, w := fetchLinkPreview(context.Background(), "see "+srv.URL+"/page", false)
		if preview != nil {
			t.Errorf("preview = %+v, want nil for a loopback target", preview)
		}
		warning = w
	})
	if stderr != "" {
		t.Fatalf("fetchLinkPreview wrote to stderr: %q", stderr)
	}
	if !strings.Contains(warning, "link preview skipped for "+srv.URL+"/page") || !strings.Contains(warning, "private or local address") {
		t.Fatalf("warning = %q", warning)
	}
}

func TestDelegatedTextPreviewWarningRendersInCaller(t *testing.T) {
	raw, err := json.Marshal(sendDelegateResponse{OK: true, Sent: true, To: "chat", ID: "id1", PreviewWarning: "link preview skipped for https://example.com: \x1b[31mboom\x1b[0m"})
	if err != nil {
		t.Fatal(err)
	}
	var resp sendDelegateResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.PreviewWarning == "" {
		t.Fatalf("preview_warning lost across the delegate socket: %s", raw)
	}

	var stdout string
	stderr := captureRootStderr(t, func() {
		stdout = captureRootStdout(t, func() {
			if err := writeDelegatedSendOutput(&rootFlags{asJSON: true}, "text", resp); err != nil {
				t.Errorf("writeDelegatedSendOutput: %v", err)
			}
		})
	})
	if strings.Contains(stderr, "\x1b") {
		t.Fatalf("warning was not sanitized: %q", stderr)
	}
	if !strings.Contains(stderr, "warning: link preview skipped for https://example.com:") || !strings.Contains(stderr, "boom") {
		t.Fatalf("caller stderr = %q", stderr)
	}
	if !strings.Contains(stderr, "message text was still sent") || !strings.Contains(stderr, "--no-preview") {
		t.Fatalf("caller stderr = %q", stderr)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		t.Fatalf("stdout is not JSON: %q", stdout)
	}
}
