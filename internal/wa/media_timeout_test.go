package wa

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewDirectMediaHTTPClientBoundsPhasesWithoutTotalBodyTimeout(t *testing.T) {
	client := newDirectMediaHTTPClient()
	if client.Timeout != 0 {
		t.Fatalf("direct media HTTP client timeout = %s, want zero to preserve caller/body budget", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("direct media HTTP client transport = %T, want *http.Transport", client.Transport)
	}
	if transport.DialContext == nil {
		t.Fatalf("dial context is nil, want default transport dialer")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = false, want default transport HTTP/2 behavior preserved")
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Fatalf("TLS handshake timeout = %s, want positive timeout", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Fatalf("response header timeout = %s, want positive timeout", transport.ResponseHeaderTimeout)
	}
	if transport.ExpectContinueTimeout <= 0 {
		t.Fatalf("expect continue timeout = %s, want positive timeout", transport.ExpectContinueTimeout)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatalf("idle connection timeout = %s, want positive timeout", transport.IdleConnTimeout)
	}
	if transport.MaxIdleConns <= 0 {
		t.Fatalf("max idle connections = %d, want positive limit", transport.MaxIdleConns)
	}
	t.Logf("direct media behavior: client.Timeout=%s, so valid response bodies keep the caller context budget", client.Timeout)
	t.Logf("direct media behavior: default transport semantics preserved: ForceAttemptHTTP2=%t DialContextPresent=%t MaxIdleConnsPerHost=%d", transport.ForceAttemptHTTP2, transport.DialContext != nil, transport.MaxIdleConnsPerHost)
	t.Logf("direct media behavior: phase bounds active: TLSHandshakeTimeout=%s ResponseHeaderTimeout=%s ExpectContinueTimeout=%s IdleConnTimeout=%s MaxIdleConns=%d", transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout, transport.ExpectContinueTimeout, transport.IdleConnTimeout, transport.MaxIdleConns)
}

func TestDownloadDirectBytesUsesDedicatedHTTPClient(t *testing.T) {
	oldClient := directMediaHTTPClient
	called := false
	directMediaHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			if got, want := req.URL.String(), "https://example.test/voice.ogg"; got != want {
				t.Fatalf("request URL = %q, want %q", got, want)
			}
			if req.Header.Get("Origin") == "" || req.Header.Get("Referer") == "" {
				t.Fatalf("missing WhatsApp media request headers")
			}
			t.Logf("direct media behavior: dedicated directMediaHTTPClient handled request host=%s path=%s origin_header_present=%t referer_header_present=%t", req.URL.Host, req.URL.Path, req.Header.Get("Origin") != "", req.Header.Get("Referer") != "")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("media bytes")),
				Request:    req,
			}, nil
		}),
	}
	defer func() {
		directMediaHTTPClient = oldClient
	}()

	got, err := downloadDirectBytes(context.Background(), "https://example.test/voice.ogg")
	if err != nil {
		t.Fatalf("downloadDirectBytes: %v", err)
	}
	if !called {
		t.Fatalf("dedicated direct media HTTP client was not used")
	}
	if string(got) != "media bytes" {
		t.Fatalf("downloadDirectBytes = %q, want media bytes", string(got))
	}
	t.Logf("direct media behavior: downloadDirectBytes used dedicated client and returned %d bytes", len(got))
}

func TestDownloadDirectBytesFailsFastWhenServerStallsBeforeHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	oldClient := directMediaHTTPClient
	directMediaHTTPClient = &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond},
	}
	defer func() {
		directMediaHTTPClient = oldClient
	}()

	start := time.Now()
	_, err := downloadDirectBytes(context.Background(), server.URL+"/voice.ogg")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("downloadDirectBytes succeeded, want response header timeout error")
	}
	if elapsed > time.Second {
		t.Fatalf("downloadDirectBytes elapsed = %s, want bounded failure under 1s", elapsed)
	}
	t.Logf("direct media behavior: pre-header stall failed in %s with error %q", elapsed, err.Error())
}

func TestDownloadDirectBytesAllowsSlowBodyAfterHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("slow body"))
	}))
	defer server.Close()

	oldClient := directMediaHTTPClient
	directMediaHTTPClient = &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond},
	}
	defer func() {
		directMediaHTTPClient = oldClient
	}()

	start := time.Now()
	got, err := downloadDirectBytes(context.Background(), server.URL+"/voice.ogg")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("downloadDirectBytes: %v", err)
	}
	if string(got) != "slow body" {
		t.Fatalf("downloadDirectBytes = %q, want slow body", string(got))
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("downloadDirectBytes elapsed = %s, want slow body delay to be observed", elapsed)
	}
	t.Logf("direct media behavior: headers arrived before the 50ms ResponseHeaderTimeout, then slow body completed in %s and returned %d bytes", elapsed, len(got))
}
