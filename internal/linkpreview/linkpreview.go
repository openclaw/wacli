package linkpreview

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	maxHTMLBytes        = 1 << 20
	maxThumbnailBytes   = 5 << 20
	smallThumbnailBytes = 300 << 10
)

var httpURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

type Preview struct {
	URL         string
	Title       string
	Description string
	Thumbnail   []byte
}

func FindFirstHTTPURL(text string) string {
	for _, match := range httpURLPattern.FindAllString(text, -1) {
		raw := trimURL(match)
		u, err := url.Parse(raw)
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return raw
		}
	}
	return ""
}

func Fetch(ctx context.Context, client *http.Client, rawURL string) (*Preview, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid preview URL")
	}
	if client == nil {
		var err error
		client, err = NewPreviewHTTPClient()
		if err != nil {
			return nil, err
		}
	}

	doc, finalURL, err := fetchHTML(ctx, client, u.String())
	if err != nil {
		return nil, err
	}

	data := scrape(doc)
	if data.Title == "" && data.Description == "" && data.ImageURL == "" {
		return nil, fmt.Errorf("preview metadata not found")
	}

	preview := &Preview{
		URL:         u.String(),
		Title:       data.Title,
		Description: data.Description,
	}
	if data.ImageURL != "" {
		if imageURL, err := finalURL.Parse(data.ImageURL); err == nil {
			preview.Thumbnail = fetchThumbnail(ctx, client, imageURL.String())
		}
	}
	return preview, nil
}

// NewSafeHTTPClient returns a client that refuses every non-public
// destination. It ignores WACLI_LINKPREVIEW_ALLOW_CIDRS, so other consumers
// (sync webhooks) keep their own authorization independent of preview policy.
func NewSafeHTTPClient() *http.Client {
	return newSafeHTTPClient(nil)
}

// NewPreviewHTTPClient is NewSafeHTTPClient plus the opt-in
// WACLI_LINKPREVIEW_ALLOW_CIDRS exceptions. Use it only for link preview
// fetches (page and image).
func NewPreviewHTTPClient() (*http.Client, error) {
	allow, err := allowedPreviewPrefixes()
	if err != nil {
		return nil, err
	}
	return newSafeHTTPClient(allow), nil
}

func newSafeHTTPClient(allow []netip.Prefix) *http.Client {
	transport := &http.Transport{
		DialContext: (&safeDialer{
			resolver: net.DefaultResolver,
			dialer:   &net.Dialer{Timeout: 5 * time.Second},
			allow:    allow,
		}).DialContext,
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("stopped after 3 redirects")
			}
			if req.URL == nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
				return fmt.Errorf("invalid redirect URL")
			}
			return nil
		},
	}
}

type safeDialer struct {
	resolver ipResolver
	dialer   contextDialer
	// allow lists CIDRs exempt from the non-public address check.
	allow []netip.Prefix
}

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

func (d *safeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := d.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	sawPublic := false
	for _, ip := range ips {
		if !safePreviewIP(ip.IP) && !allowedByPrefixes(ip.IP, d.allow) {
			continue
		}
		sawPublic = true
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if sawPublic && lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("preview URL resolves to a private or local address")
}

func safePreviewIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	return addr.IsGlobalUnicast() &&
		!addr.IsPrivate() &&
		!addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() &&
		!addr.IsMulticast() &&
		!addr.IsUnspecified() &&
		!reservedPreviewIP(addr)
}

// allowedPreviewPrefixes reads WACLI_LINKPREVIEW_ALLOW_CIDRS, a comma-separated
// list of CIDRs that bypass the private/reserved address check. This is needed
// behind fake-IP DNS proxies (e.g. 198.18.0.0/15) where every host resolves to
// a reserved range.
func allowedPreviewPrefixes() ([]netip.Prefix, error) {
	rawList := strings.TrimSpace(os.Getenv("WACLI_LINKPREVIEW_ALLOW_CIDRS"))
	if rawList == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for i, raw := range strings.Split(rawList, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || p.Addr().Is4In6() || p != p.Masked() {
			return nil, fmt.Errorf("invalid WACLI_LINKPREVIEW_ALLOW_CIDRS entry %d: use a canonical IPv4 or IPv6 network CIDR", i+1)
		}
		out = append(out, p)
	}
	return out, nil
}

func allowedByPrefixes(ip net.IP, allow []netip.Prefix) bool {
	if len(allow) == 0 {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range allow {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func reservedPreviewIP(addr netip.Addr) bool {
	for _, prefix := range reservedPreviewPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

var reservedPreviewPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func trimURL(raw string) string {
	raw = strings.TrimRight(raw, ".,!?;:")
	for {
		if strings.HasSuffix(raw, ")") && strings.Count(raw, "(") < strings.Count(raw, ")") {
			raw = strings.TrimSuffix(raw, ")")
			continue
		}
		if strings.HasSuffix(raw, "]") && strings.Count(raw, "[") < strings.Count(raw, "]") {
			raw = strings.TrimSuffix(raw, "]")
			continue
		}
		if strings.HasSuffix(raw, "}") && strings.Count(raw, "{") < strings.Count(raw, "}") {
			raw = strings.TrimSuffix(raw, "}")
			continue
		}
		return raw
	}
}

func fetchHTML(ctx context.Context, client *http.Client, rawURL string) (*html.Node, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "wacli")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("preview request failed: %s", resp.Status)
	}

	limited := io.LimitReader(resp.Body, maxHTMLBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxHTMLBytes {
		return nil, nil, fmt.Errorf("preview HTML too large")
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, err
	}
	return doc, resp.Request.URL, nil
}

func fetchThumbnail(ctx context.Context, client *http.Client, rawURL string) []byte {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "wacli")

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(thumbnailByteLimit())+1))
	if err != nil || len(body) > thumbnailByteLimit() {
		return nil
	}
	return body
}

type metadata struct {
	Title       string
	Description string
	ImageURL    string
}

func scrape(node *html.Node) metadata {
	var data metadata
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "meta":
				applyMeta(&data, n)
			case "title":
				if data.Title == "" {
					data.Title = nodeText(n)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	data.Title = clean(data.Title)
	data.Description = clean(data.Description)
	data.ImageURL = clean(data.ImageURL)
	return data
}

func applyMeta(data *metadata, n *html.Node) {
	key := strings.ToLower(firstAttr(n, "property"))
	if key == "" {
		key = strings.ToLower(firstAttr(n, "name"))
	}
	content := clean(firstAttr(n, "content"))
	if key == "" || content == "" {
		return
	}

	switch key {
	case "og:title", "twitter:title":
		data.Title = pick(data.Title, content)
	case "og:description", "twitter:description", "description":
		data.Description = pick(data.Description, content)
	case "og:image", "og:image:url", "twitter:image", "twitter:image:src":
		data.ImageURL = pick(data.ImageURL, content)
	}
}

func firstAttr(n *html.Node, name string) string {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}

func clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func pick(current, next string) string {
	if current != "" {
		return current
	}
	return next
}

// SmallThumbnails restores inline-only previews and the legacy download cap.
// The sync owner's environment controls delegated sends.
func SmallThumbnails() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WACLI_LINKPREVIEW_SMALL"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func thumbnailByteLimit() int {
	if SmallThumbnails() {
		return smallThumbnailBytes
	}
	return maxThumbnailBytes
}
