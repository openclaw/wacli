package wa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewDirectMediaHTTPClientHasTimeouts(t *testing.T) {
	client := newDirectMediaHTTPClient()
	if client.Timeout <= 0 {
		t.Fatalf("direct media HTTP client timeout = %s, want positive timeout", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("direct media HTTP client transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Fatalf("response header timeout = %s, want positive timeout", transport.ResponseHeaderTimeout)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatalf("idle connection timeout = %s, want positive timeout", transport.IdleConnTimeout)
	}
	if transport.MaxIdleConns <= 0 {
		t.Fatalf("max idle connections = %d, want positive limit", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost <= 0 {
		t.Fatalf("max idle connections per host = %d, want positive limit", transport.MaxIdleConnsPerHost)
	}
}

func TestDownloadDirectBytesUsesBoundedHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	oldClient := directMediaHTTPClient
	directMediaHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	defer func() {
		directMediaHTTPClient = oldClient
	}()

	start := time.Now()
	_, err := downloadDirectBytes(context.Background(), server.URL+"/voice.ogg")
	if err == nil {
		t.Fatalf("downloadDirectBytes succeeded, want timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("downloadDirectBytes elapsed = %s, want bounded failure under 1s", elapsed)
	}
}
