package linkpreview

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFindFirstHTTPURL(t *testing.T) {
	got := FindFirstHTTPURL(`See (https://example.com/path?q=1), then http://later.test.`)
	if got != "https://example.com/path?q=1" {
		t.Fatalf("url = %q", got)
	}
}

func TestFetchScrapesOpenGraphMetadataAndThumbnail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
<meta property="og:title" content="OG Title">
<meta property="og:description" content="OG Description">
<meta property="og:image" content="/thumb.jpg">
</head></html>`))
	})
	mux.HandleFunc("/thumb.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.URL != srv.URL {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Title != "OG Title" {
		t.Fatalf("Title = %q", got.Title)
	}
	if got.Description != "OG Description" {
		t.Fatalf("Description = %q", got.Description)
	}
	if string(got.Thumbnail) != "jpeg" {
		t.Fatalf("Thumbnail = %q", string(got.Thumbnail))
	}
}

func TestFetchReportsMissingMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head></head><body>empty</body></html>`))
	}))
	t.Cleanup(srv.Close)

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatalf("expected missing metadata error")
	}
}

func TestFetchFallsBackToTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>  Plain   Title  </title></head></html>`))
	}))
	t.Cleanup(srv.Close)

	got, err := Fetch(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Title != "Plain Title" {
		t.Fatalf("Title = %q", got.Title)
	}
}

func TestFetchRejectsInvalidURL(t *testing.T) {
	if _, err := Fetch(context.Background(), nil, "file:///tmp/page.html"); err == nil {
		t.Fatalf("expected invalid URL error")
	}
}

func TestFetchDefaultClientRejectsLocalhost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Local</title></head></html>`))
	}))
	t.Cleanup(srv.Close)

	if _, err := Fetch(context.Background(), nil, srv.URL); err == nil {
		t.Fatalf("expected localhost fetch to be rejected")
	}
}

func TestFetchDefaultClientIgnoresProxyForPrivateTargets(t *testing.T) {
	proxyCalled := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalled = true
		_, _ = w.Write([]byte(`<html><head><title>Proxy</title></head></html>`))
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	if _, err := Fetch(context.Background(), nil, "http://10.0.0.1/"); err == nil {
		t.Fatalf("expected private target to be rejected")
	}
	if proxyCalled {
		t.Fatalf("safe client used HTTP_PROXY for a private target")
	}
}

func TestSafeDialerTriesRemainingPublicIPs(t *testing.T) {
	wantErr := errors.New("first failed")
	d := &safeDialer{
		resolver: fakeResolver{ips: []net.IPAddr{
			{IP: net.ParseIP("203.0.113.1")},
			{IP: net.ParseIP("93.184.216.34")},
		}},
		dialer: &fakeDialer{
			failByAddress: map[string]error{
				"93.184.216.34:443": wantErr,
			},
		},
	}
	conn, err := d.DialContext(context.Background(), "tcp", "example.com:443")
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected first public dial error when only reserved address precedes it")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("DialContext error = %v, want %v", err, wantErr)
	}

	d.dialer = &fakeDialer{
		failByAddress: map[string]error{
			"93.184.216.34:443": wantErr,
		},
		successAddress: "142.250.72.14:443",
	}
	d.resolver = fakeResolver{ips: []net.IPAddr{
		{IP: net.ParseIP("93.184.216.34")},
		{IP: net.ParseIP("142.250.72.14")},
	}}
	conn, err = d.DialContext(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()
}

func TestSafePreviewIPRejectsNonPublicRanges(t *testing.T) {
	for _, raw := range []string{
		"10.0.0.1",
		"100.64.0.1",
		"169.254.169.254",
		"192.0.2.1",
		"198.18.0.1",
		"198.51.100.1",
		"203.0.113.1",
		"240.0.0.1",
		"::10.0.0.1",
		"64:ff9b::a00:1",
		"2002:a00:1::",
		"2001:db8::1",
		"fc00::1",
		"fec0::1",
	} {
		if safePreviewIP(net.ParseIP(raw)) {
			t.Fatalf("safePreviewIP(%s) = true, want false", raw)
		}
	}
	if !safePreviewIP(net.ParseIP("93.184.216.34")) {
		t.Fatalf("expected public IPv4 to be allowed")
	}
	if !safePreviewIP(net.ParseIP("2606:4700:4700::1111")) {
		t.Fatalf("expected public IPv6 to be allowed")
	}
}

type fakeResolver struct {
	ips []net.IPAddr
	err error
}

func (r fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.ips, r.err
}

type fakeDialer struct {
	failByAddress  map[string]error
	successAddress string
}

func (d *fakeDialer) DialContext(_ context.Context, _ string, address string) (net.Conn, error) {
	if err := d.failByAddress[address]; err != nil {
		return nil, err
	}
	if d.successAddress != "" && address != d.successAddress {
		return nil, errors.New("unexpected address")
	}
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, nil
}

func TestPreviewAllowCIDRsIsOptIn(t *testing.T) {
	fakeIP := net.ParseIP("198.18.0.42")
	t.Setenv("WACLI_LINKPREVIEW_ALLOW_CIDRS", "")
	allow, err := allowedPreviewPrefixes()
	if err != nil {
		t.Fatal(err)
	}
	if safePreviewIP(fakeIP) || allowedByPrefixes(fakeIP, allow) {
		t.Fatalf("fake-IP range must be rejected by default")
	}
	t.Setenv("WACLI_LINKPREVIEW_ALLOW_CIDRS", " 198.18.0.0/15 ")
	allow, err = allowedPreviewPrefixes()
	if err != nil {
		t.Fatal(err)
	}
	if !allowedByPrefixes(fakeIP, allow) {
		t.Fatalf("expected allowlisted fake-IP range to be allowed")
	}
	if allowedByPrefixes(net.ParseIP("10.0.0.1"), allow) || allowedByPrefixes(net.ParseIP("127.0.0.1"), allow) {
		t.Fatalf("allowlist must not open other private ranges")
	}
	if safePreviewIP(fakeIP) {
		t.Fatalf("safePreviewIP must not read the preview allowlist")
	}
}

func TestSafeDialerAllowListOnlyAppliesWhenGiven(t *testing.T) {
	resolver := fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("198.18.0.42")}}}
	t.Setenv("WACLI_LINKPREVIEW_ALLOW_CIDRS", "198.18.0.0/15")

	strict := &safeDialer{resolver: resolver, dialer: &fakeDialer{}}
	if conn, err := strict.DialContext(context.Background(), "tcp", "example.com:443"); err == nil {
		_ = conn.Close()
		t.Fatalf("dialer without an allow list must ignore WACLI_LINKPREVIEW_ALLOW_CIDRS")
	}

	allow, err := allowedPreviewPrefixes()
	if err != nil {
		t.Fatal(err)
	}
	preview := &safeDialer{resolver: resolver, dialer: &fakeDialer{}, allow: allow}
	conn, err := preview.DialContext(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("preview dialer with allow list: %v", err)
	}
	_ = conn.Close()
}

func TestPreviewAllowCIDRsRejectsEntireInvalidConfiguration(t *testing.T) {
	for _, value := range []string{"invalid", "198.18.0.0/15,invalid", "127.0.0.1", "198.18.1.1/15", "198.18.0.0/15,", ",198.18.0.0/15", "::ffff:127.0.0.0/104"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("WACLI_LINKPREVIEW_ALLOW_CIDRS", value)
			allow, err := allowedPreviewPrefixes()
			if err == nil || len(allow) != 0 {
				t.Fatalf("prefixes=%v err=%v", allow, err)
			}
			if client, err := NewPreviewHTTPClient(); err == nil || client != nil {
				t.Fatalf("client=%v err=%v", client, err)
			}
			// An explicitly supplied client must remain usable by non-preview consumers.
			if NewSafeHTTPClient() == nil {
				t.Fatal("strict client unavailable")
			}
		})
	}
}
