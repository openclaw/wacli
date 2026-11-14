//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCert"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"google.golang.org/protobuf/proto"
)

// Real `wacli sync --follow` owner (App.Sync + the delegate socket) connected to
// an in-process peer over Noise XX/WebSocket, then the real Cobra commands
// `profile business get|set` run against the same store and must be executed by
// that owner over its send socket. The peer answers the w:biz get/set stanzas the
// production code sends from an in-memory profile and logs every stanza and
// connection it sees, so the log shows the delta on the wire, the owner doing
// the work (one connection for the whole test) and the profile state after each
// step. The Noise responder is a copy of the one in
// internal/wa/own_message_transport_test.go (that package cannot be imported
// from here).
//
// With WACLI_PROOF_STABLE_WACLI=<official v0.26.0 binary> the store's wacli.db
// and session.db are first created by that binary; the head then opens them.
//
// Not proven: pairing/login, WhatsApp's own validation or moderation of profile
// fields, the phone UI, a real Business account. The peer is a model of the
// stanzas, not the service. Synthetic identifiers only; no run may be parallel
// (SDK trust root and socket URL are process-wide).
func TestProfileBusinessViaOwner(t *testing.T) {
	root := keys.NewKeyPair()
	prevRoot := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = prevRoot })
	// Not derived from t.Context(): that is cancelled before cleanups run, and the
	// peer must keep answering while the owner shuts down.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	dir, err := os.MkdirTemp("", "wacli-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	prepareStore(t, ctx, dir)

	static := keys.NewKeyPair()
	peer := newBizPeer(t, ctx, static, bizCertificate(t, root, static))
	server := httptest.NewTLSServer(http.HandlerFunc(peer.serve))
	t.Cleanup(server.Close)
	routeToLocal(t, server, "web.whatsapp.com:443")
	t.Log("ROUTE the SDK dials its real URL wss://web.whatsapp.com/ws/chat; the process-wide default transport sends that host to the local peer and trusts only the test certificate")

	ownerDone := make(chan error, 1)
	go func() {
		ownerDone <- execute([]string{"--store", dir, "sync", "--follow", "--presence-mode", "quiet"})
	}()
	stopped := false
	stopOwner := func() {
		if stopped {
			return
		}
		stopped = true
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		select {
		case err := <-ownerDone:
			t.Logf("OWNER stopped: sync exited err=%v", err)
		case <-time.After(20 * time.Second):
			t.Error("owner did not stop")
		}
	}
	t.Cleanup(stopOwner)
	waitFor(t, ctx, "owner socket and Noise connection", func() bool {
		info, err := os.Stat(filepath.Join(dir, ".send.sock"))
		return err == nil && info.Mode()&os.ModeSocket != 0 && peer.connections() == 1
	})
	t.Logf("OWNER up: `wacli sync --follow` holds the store, Noise XX verified (test CA), send socket listening, peer connections=%d", peer.connections())
	if l, err := lock.Acquire(dir); err == nil {
		_ = l.Release()
		t.Fatal("store lock is free while the owner runs")
	} else {
		t.Logf("OWNER holds the store lock: a second opener is refused (lock contention=%v), so the CLI can only delegate", lock.IsLocked(err))
	}
	linked := "ownerline@s.whatsapp.net"

	invocations := 0
	cli := func(label string, args ...string) (string, string, error) {
		t.Helper()
		invocations++
		t.Logf("$ wacli %s   # %s", strings.Join(args, " "), label)
		stdout, stderr, err := runCLI(t, append([]string{"--store", dir}, args...))
		for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
			if line != "" {
				t.Logf("  stdout| %s", line)
			}
		}
		for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
			if line != "" {
				t.Logf("  stderr| %s", line)
			}
		}
		t.Logf("  exit: err=%v   peer connections=%d", err, peer.connections())
		return stdout, stderr, err
	}
	readProfile := func(label string) bizFullProfile {
		t.Helper()
		stdout, _, err := cli(label, "--json", "profile", "business", "get")
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Data bizFullProfile `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &env); err != nil {
			t.Fatalf("get output: %v: %s", err, stdout)
		}
		return env.Data
	}
	base := peer.snapshot()

	// 1. Full read through the owner.
	got := readProfile("full read through the owner")
	if got.JID != linked || got.Description != base.description || got.Address != base.address || got.Email != base.email ||
		strings.Join(got.Websites, ",") != strings.Join(base.websites, ",") || len(got.Categories) != 1 || len(got.BusinessHours) != 3 || got.BusinessHoursTimeZone != "America/Lima" {
		t.Fatalf("full read = %+v", got)
	}
	if reqs := peer.requests(); len(reqs) != 1 || !strings.HasPrefix(reqs[0], "get "+linked) {
		t.Fatalf("peer requests = %v", reqs)
	}
	t.Log("CHECK full read: description, address, email, 2 websites, category and 3 hours rows came back through the owner's connection")

	// 2. One flag: only that field is on the wire, everything else survives.
	if _, _, err := cli("set ONE field", "profile", "business", "set", "--description", "Synthetic shop, new description"); err != nil {
		t.Fatal(err)
	}
	if sets := peer.sets(); len(sets) != 1 || strings.Join(sets[0], ",") != "description" {
		t.Fatalf("delta children on the wire = %v, want [description]", sets)
	}
	after := peer.snapshot()
	if after.description != "Synthetic shop, new description" || after.address != base.address || after.email != base.email ||
		strings.Join(after.websites, ",") != strings.Join(base.websites, ",") || after.hoursTZ != base.hoursTZ || len(after.hours) != len(base.hours) {
		t.Fatalf("peer profile after one-field delta = %+v", after)
	}
	got = readProfile("next read: omitted fields survived the delta")
	if got.Description != after.description || got.Address != base.address || got.Email != base.email || len(got.Websites) != 2 || len(got.BusinessHours) != 3 {
		t.Fatalf("read after delta = %+v", got)
	}
	t.Log("CHECK delta: the stanza carried <description> only; address, email, 2 websites and 3 hours rows are unchanged on the peer and in the next read")

	// 3. Website clearing.
	if _, _, err := cli("website clearing (a single empty value)", "profile", "business", "set", "--website", ""); err != nil {
		t.Fatal(err)
	}
	if sets := peer.sets(); len(sets) != 2 || strings.Join(sets[1], ",") != "website" {
		t.Fatalf("delta children on the wire = %v, want [description] then [website]", sets)
	}
	cleared := peer.snapshot()
	if len(cleared.websites) != 0 || cleared.description != after.description || cleared.address != base.address || cleared.email != base.email || len(cleared.hours) != 3 {
		t.Fatalf("peer profile after clearing = %+v", cleared)
	}
	got = readProfile("next read: websites gone, nothing else lost")
	if len(got.Websites) != 0 || got.Description != after.description || got.Address != base.address || got.Email != base.email {
		t.Fatalf("read after clearing = %+v", got)
	}
	t.Log("CHECK website clearing: one empty <website/> on the wire; the websites are gone and description, address, email and hours are intact")

	// 4. Re-read failure: the update succeeds, the warning goes to stderr.
	peer.failNextGetAfterSet()
	stdout, stderr, err := cli("update accepted but the follow-up read fails", "profile", "business", "set", "--address", "2 Synthetic Avenue")
	if err != nil {
		t.Fatalf("update must stay successful: %v", err)
	}
	if !strings.Contains(stdout, "Business profile updated.") || strings.Contains(stdout, "Could not re-read") ||
		!strings.Contains(stderr, "Could not re-read the profile:") || !strings.Contains(stderr, "wacli profile business get") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if peer.snapshot().address != "2 Synthetic Avenue" {
		t.Fatal("peer did not apply the address delta")
	}
	got = readProfile("next read shows the accepted update")
	if got.Address != "2 Synthetic Avenue" {
		t.Fatalf("read after warning = %+v", got)
	}
	t.Log("CHECK re-read warning: exit 0, 'Business profile updated.' on stdout, 'Could not re-read the profile' + recovery hint on stderr only; the next get shows the new address")

	// 5. Everything ran on the owner's single connection.
	stopOwner()
	if n := peer.connections(); n != 1 {
		t.Fatalf("peer saw %d connections, want 1", n)
	}
	t.Logf("STORE migrations in the stable-created wacli.db after the head owner ran on it: %d", migrationCount(t, filepath.Join(dir, "wacli.db")))
	t.Logf("CHECK owner: %d CLI invocations produced %d w:biz stanzas (every set is followed by a re-read get), all on connection #1 (the owner's); no CLI invocation opened a WhatsApp connection", invocations, len(peer.requests()))
}

// prepareStore creates wacli.db and session.db, with the official stable binary
// when WACLI_PROOF_STABLE_WACLI is set, and pairs a synthetic device in
// session.db the way a linked line would have one.
func prepareStore(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	if bin := os.Getenv("WACLI_PROOF_STABLE_WACLI"); bin != "" {
		raw, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		version, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("STORE created by the stable release: binary sha256=%s version=%s", hex.EncodeToString(sum[:]), strings.TrimSpace(string(version)))
		status, err := exec.CommandContext(ctx, bin, "--store", dir, "--json", "auth", "status").CombinedOutput()
		if err != nil {
			t.Fatalf("stable binary: %v: %s", err, status)
		}
		t.Logf("STORE stable `auth status` (creates wacli.db + session.db): %s", strings.TrimSpace(string(status)))
	} else {
		t.Log("STORE stable binary not provided (WACLI_PROOF_STABLE_WACLI unset): the store is created by this build")
	}
	t.Logf("STORE migrations in wacli.db before the head opens it: %d", migrationCount(t, filepath.Join(dir, "wacli.db")))
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(dir, "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	device := container.NewDevice()
	device.ID = &types.JID{User: "ownerline", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	_ = container.Close()
	t.Log("STORE synthetic paired device ownerline:7 saved in session.db (pairing itself is not exercised)")
}

func migrationCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		return -1
	}
	return n
}

// routeToLocal makes the process-wide default transport (which whatsmeow clones
// into its websocket client when the client is built) dial only host, at the
// local TLS server, trusting only its certificate. Restored on cleanup.
func routeToLocal(t *testing.T, server *httptest.Server, host string) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	prev := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != host {
				return nil, fmt.Errorf("unexpected dial %s", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
	t.Cleanup(func() { http.DefaultTransport = prev })
}

func waitFor(t *testing.T, ctx context.Context, what string, ready func() bool) {
	t.Helper()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// runCLI runs the real root command in-process with stdout/stderr captured.
func runCLI(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = outW, errW
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()
	runErr := execute(args)
	os.Stdout, os.Stderr = origOut, origErr
	_ = outW.Close()
	_ = errW.Close()
	wg.Wait()
	return outBuf.String(), errBuf.String(), runErr
}

// Wire shapes of the CLI's --json output, declared here so the trace compiles
// (and fails on behaviour) against a tree without the commands.
type bizDay struct {
	DayOfWeek string `json:"day_of_week"`
	Mode      string `json:"mode"`
	OpenTime  string `json:"open_time,omitempty"`
	CloseTime string `json:"close_time,omitempty"`
}

type bizFullProfile struct {
	JID         string   `json:"jid"`
	Description string   `json:"description"`
	Address     string   `json:"address"`
	Email       string   `json:"email"`
	Websites    []string `json:"websites"`
	Categories  []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"categories"`
	BusinessHoursTimeZone string   `json:"business_hours_timezone"`
	BusinessHours         []bizDay `json:"business_hours"`
}

type bizCatalogPage struct {
	HasCatalog bool `json:"has_catalog"`
	Products   []struct {
		ID string `json:"id"`
	} `json:"products"`
	NextCursor string `json:"next_cursor"`
}

type bizProfile struct {
	description, address, email string
	websites                    []string
	hoursTZ                     string
	hours                       []bizDay
}

type bizPeer struct {
	t       *testing.T
	ctx     context.Context
	static  *keys.KeyPair
	cert    []byte
	mu      sync.Mutex
	conns   int
	profile bizProfile
	reqs    []string
	deltas  [][]string
	failGet bool
}

func newBizPeer(t *testing.T, ctx context.Context, static *keys.KeyPair, cert []byte) *bizPeer {
	return &bizPeer{t: t, ctx: ctx, static: static, cert: cert, profile: bizProfile{
		description: "Synthetic camera shop",
		address:     "1 Synthetic Street",
		email:       "shop@example.test",
		websites:    []string{"https://shop.example.test", "https://example.test/catalog"},
		hoursTZ:     "America/Lima",
		hours: []bizDay{
			{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "540", CloseTime: "1080"},
			{DayOfWeek: "tue", Mode: "specific_hours", OpenTime: "540", CloseTime: "1080"},
			{DayOfWeek: "sat", Mode: "open_24h"},
		},
	}}
}

func (p *bizPeer) connections() int { p.mu.Lock(); defer p.mu.Unlock(); return p.conns }
func (p *bizPeer) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.reqs...)
}
func (p *bizPeer) sets() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.deltas...)
}
func (p *bizPeer) failNextGetAfterSet() { p.mu.Lock(); defer p.mu.Unlock(); p.failGet = true }
func (p *bizPeer) snapshot() bizProfile {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.profile
	out.websites = append([]string(nil), p.profile.websites...)
	out.hours = append([]bizDay(nil), p.profile.hours...)
	return out
}

var wireSpace = regexp.MustCompile(`\s*\n\s*`)

func wireText(n waBinary.Node) string { return wireSpace.ReplaceAllString(n.String(), "") }

func (p *bizPeer) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		p.t.Error(err)
		return
	}
	defer conn.CloseNow()
	noise, err := bizHandshake(p.ctx, conn, p.static, p.cert)
	if err != nil {
		p.t.Errorf("Noise handshake: %v", err)
		return
	}
	p.mu.Lock()
	p.conns++
	id := p.conns
	p.mu.Unlock()
	p.t.Logf("PEER conn#%d: Noise XX handshake verified; synthetic paired device connected", id)
	for {
		node, err := noise.read(p.ctx)
		if err != nil {
			p.t.Logf("PEER conn#%d: closed (%v)", id, err)
			return
		}
		if node.Tag != "iq" {
			continue
		}
		reply := waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "type": "result", "from": types.ServerJID}}
		if node.Attrs["xmlns"] == "w:biz" {
			reply = p.business(id, *node, reply)
		} else if collection, ok := node.GetOptionalChildByTag("sync", "collection"); ok {
			reply.Content = []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": collection.Attrs["name"], "has_more_patches": "false"}}}}}
		}
		if err := noise.write(p.ctx, reply); err != nil {
			return
		}
	}
}

func (p *bizPeer) business(conn int, iq waBinary.Node, reply waBinary.Node) waBinary.Node {
	p.mu.Lock()
	defer p.mu.Unlock()
	bp := iq.GetChildByTag("business_profile")
	switch iq.Attrs["type"] {
	case "get":
		jid := fmt.Sprint(bp.GetChildByTag("profile").Attrs["jid"])
		p.reqs = append(p.reqs, "get "+jid)
		p.t.Logf("PEER conn#%d <- %s", conn, wireText(iq))
		if p.failGet {
			p.failGet = false
			p.t.Logf("PEER conn#%d -> iq error 500 (injected: the follow-up read after a set fails)", conn)
			reply.Attrs["type"] = "error"
			reply.Content = []waBinary.Node{{Tag: "error", Attrs: waBinary.Attrs{"code": "500", "text": "internal-server-error"}}}
			return reply
		}
		reply.Content = []waBinary.Node{{Tag: "business_profile", Attrs: waBinary.Attrs{"v": "244"}, Content: []waBinary.Node{p.profileNode(jid)}}}
		p.t.Logf("PEER conn#%d -> %s", conn, wireText(reply))
	case "set":
		var tags []string
		for _, child := range bp.GetChildren() {
			tags = append(tags, child.Tag)
		}
		p.reqs = append(p.reqs, "set "+strings.Join(tags, ","))
		p.deltas = append(p.deltas, tags)
		p.t.Logf("PEER conn#%d <- %s", conn, wireText(iq))
		if bp.Attrs["mutation_type"] != "delta" || bp.Attrs["v"] != "3" {
			p.t.Errorf("set is not a v3 delta: %v", bp.Attrs)
		}
		websites := []string(nil)
		sawWebsite := false
		for _, child := range bp.GetChildren() {
			text, _ := child.Content.([]byte)
			switch child.Tag {
			case "description":
				p.profile.description = string(text)
			case "address":
				p.profile.address = string(text)
			case "email":
				p.profile.email = string(text)
			case "website":
				sawWebsite = true
				if len(text) > 0 {
					websites = append(websites, string(text))
				}
			}
		}
		if sawWebsite {
			p.profile.websites = websites
		}
		p.t.Logf("PEER profile now: description=%q address=%q email=%q websites=%d hours=%d (tz %s)", p.profile.description, p.profile.address, p.profile.email, len(p.profile.websites), len(p.profile.hours), p.profile.hoursTZ)
		reply.Content = []waBinary.Node{{Tag: "business_profile", Attrs: waBinary.Attrs{"v": "3"}}}
	default:
		p.t.Errorf("unexpected w:biz type %v", iq.Attrs["type"])
	}
	return reply
}

func (p *bizPeer) profileNode(jid string) waBinary.Node {
	text := func(tag, value string) waBinary.Node { return waBinary.Node{Tag: tag, Content: []byte(value)} }
	children := []waBinary.Node{text("description", p.profile.description), text("address", p.profile.address), text("email", p.profile.email)}
	for _, site := range p.profile.websites {
		children = append(children, text("website", site))
	}
	children = append(children, waBinary.Node{Tag: "categories", Content: []waBinary.Node{{Tag: "category", Attrs: waBinary.Attrs{"id": "1000"}, Content: []byte("Synthetic Retail")}}})
	configs := []waBinary.Node{}
	for _, day := range p.profile.hours {
		attrs := waBinary.Attrs{"day_of_week": day.DayOfWeek, "mode": day.Mode}
		if day.Mode == "specific_hours" {
			attrs["open_time"], attrs["close_time"] = day.OpenTime, day.CloseTime
		}
		configs = append(configs, waBinary.Node{Tag: "business_hours_config", Attrs: attrs})
	}
	children = append(children, waBinary.Node{Tag: "business_hours", Attrs: waBinary.Attrs{"timezone": p.profile.hoursTZ}, Content: configs})
	parsed, _ := types.ParseJID(jid)
	return waBinary.Node{Tag: "profile", Attrs: waBinary.Attrs{"jid": parsed}, Content: children}
}

// TestCatalogListCursorsOverTLS runs the real `catalog list` Cobra command
// against a local TLS server standing in for graph.whatsapp.com (the default
// transport is redirected there and trusts only the test certificate): page 1
// returns a cursor, passing it with --after returns a different, final page.
// It does not prove WhatsApp's endpoint, token or persisted query id.
func TestCatalogListCursorsOverTLS(t *testing.T) {
	const cursor = "CURSOR-PAGE-2"
	var mu sync.Mutex
	var seen []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables struct {
				Request struct {
					Catalog struct {
						JID   string  `json:"jid"`
						Limit string  `json:"limit"`
						After *string `json:"after"`
					} `json:"product_catalog"`
				} `json:"request"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.URL.Path != "/graphql/catalog" || r.Host != "graph.whatsapp.com" {
			t.Errorf("bad catalog request host=%s path=%s err=%v", r.Host, r.URL.Path, err)
		}
		req := body.Variables.Request.Catalog
		after := ""
		if req.After != nil {
			after = *req.After
		}
		mu.Lock()
		seen = append(seen, after)
		mu.Unlock()
		t.Logf("TLS SERVER <- POST https://%s%s jid=%s limit=%s after=%q", r.Host, r.URL.Path, req.JID, req.Limit, after)
		product := func(id, name string) string {
			return fmt.Sprintf(`{"id":%q,"retailer_id":"","name":%q,"description":"","url":"","currency":"USD","price":"1250000","status_info":{"status":"APPROVED"},"media":{"images":[]}}`, id, name)
		}
		switch after {
		case "":
			fmt.Fprintf(w, `{"data":{"xwa_product_catalog_get_product_catalog":{"__typename":"XWAProductCatalogGetResponseSuccess","product_catalog":{"products":[%s,%s],"paging":{"before":"","after":%q}}}}}`, product("p1", "Synthetic one"), product("p2", "Synthetic two"), cursor)
		case cursor:
			fmt.Fprintf(w, `{"data":{"xwa_product_catalog_get_product_catalog":{"__typename":"XWAProductCatalogGetResponseSuccess","product_catalog":{"products":[%s],"paging":{"before":%q,"after":""}}}}}`, product("p3", "Synthetic three"), cursor)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	routeToLocal(t, server, "graph.whatsapp.com:443")

	list := func(args ...string) bizCatalogPage {
		t.Helper()
		args = append([]string{"--json", "catalog", "list", "--jid", "catalogshop@s.whatsapp.net"}, args...)
		t.Logf("$ wacli %s   # empty scratch store, no session", strings.Join(args, " "))
		stdout, stderr, err := runCLI(t, append([]string{"--store", t.TempDir()}, args...))
		if err != nil {
			t.Fatalf("%v: %s", err, stderr)
		}
		var env struct {
			Data bizCatalogPage `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &env); err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, product := range env.Data.Products {
			ids = append(ids, product.ID)
		}
		t.Logf("  result: has_catalog=%v products=%v next_cursor=%q", env.Data.HasCatalog, ids, env.Data.NextCursor)
		return env.Data
	}
	first := list("--limit", "2")
	if !first.HasCatalog || len(first.Products) != 2 || first.NextCursor != cursor {
		t.Fatalf("first page = %+v", first)
	}
	second := list("--limit", "2", "--after", first.NextCursor)
	if len(second.Products) != 1 || second.NextCursor != "" || second.Products[0].ID == first.Products[0].ID || second.Products[0].ID == first.Products[1].ID {
		t.Fatalf("second page = %+v", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "" || seen[1] != cursor {
		t.Fatalf("server saw cursors %q", seen)
	}
	t.Log("CHECK catalog: page 1 (2 products) returned a cursor; --after <cursor> fetched page 2 (1 different product, no further cursor); both requests went to graph.whatsapp.com over verified TLS")
}

func bizCertificate(t *testing.T, root, static *keys.KeyPair) []byte {
	t.Helper()
	intermediate := keys.NewKeyPair()
	issue := func(signer *keys.KeyPair, key []byte, serial, issuer uint32) *waCert.CertChain_NoiseCertificate {
		details, err := proto.Marshal(&waCert.CertChain_NoiseCertificate_Details{Serial: proto.Uint32(serial), IssuerSerial: proto.Uint32(issuer), Key: key, NotBefore: proto.Uint64(1), NotAfter: proto.Uint64(1 << 40)})
		if err != nil {
			t.Fatal(err)
		}
		signature := ecc.CalculateSignature(ecc.NewDjbECPrivateKey(*signer.Priv), details)
		return &waCert.CertChain_NoiseCertificate{Details: details, Signature: signature[:]}
	}
	cert, err := proto.Marshal(&waCert.CertChain{Intermediate: issue(root, intermediate.Pub[:], 1, whatsmeow.WACertIssuerSerial), Leaf: issue(intermediate, static.Pub[:], 2, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

type bizNoise struct {
	conn                    *websocket.Conn
	readCipher, writeCipher cipher.AEAD
	readCount, writeCount   uint32
	writeMu                 sync.Mutex
}

// Copy of ownTransportHandshake (internal/wa/own_message_transport_test.go).
func bizHandshake(ctx context.Context, conn *websocket.Conn, static *keys.KeyPair, cert []byte) (*bizNoise, error) {
	_, hello, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(hello, socket.WAConnHeader) {
		return nil, fmt.Errorf("missing WA header")
	}
	hello, err = bizFrame(hello[len(socket.WAConnHeader):])
	if err != nil {
		return nil, err
	}
	var message waWa6.HandshakeMessage
	if err := proto.Unmarshal(hello, &message); err != nil {
		return nil, err
	}
	clientEphemeral := message.GetClientHello().GetEphemeral()
	if len(clientEphemeral) != 32 {
		return nil, fmt.Errorf("bad client ephemeral")
	}
	ephemeral := keys.NewKeyPair()
	nh := socket.NewNoiseHandshake()
	nh.Start(socket.NoiseStartPattern, socket.WAConnHeader)
	nh.Authenticate(clientEphemeral)
	nh.Authenticate(ephemeral.Pub[:])
	salt := []byte(socket.NoiseStartPattern)
	mix := func(priv *[32]byte, pub []byte) error {
		secret, err := curve25519.X25519(priv[:], pub)
		if err != nil {
			return err
		}
		kdf := hkdf.New(sha256.New, secret, salt, nil)
		salt = make([]byte, 32)
		if _, err := io.ReadFull(kdf, salt); err != nil {
			return err
		}
		return nh.MixIntoKey(secret)
	}
	if err := mix(ephemeral.Priv, clientEphemeral); err != nil {
		return nil, err
	}
	staticCiphertext := nh.Encrypt(static.Pub[:])
	if err := mix(static.Priv, clientEphemeral); err != nil {
		return nil, err
	}
	response, err := proto.Marshal(&waWa6.HandshakeMessage{ServerHello: &waWa6.HandshakeMessage_ServerHello{Ephemeral: ephemeral.Pub[:], Static: staticCiphertext, Payload: nh.Encrypt(cert)}})
	if err != nil {
		return nil, err
	}
	if err := bizWriteFrame(ctx, conn, response); err != nil {
		return nil, err
	}
	_, finish, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	finish, err = bizFrame(finish)
	if err != nil {
		return nil, err
	}
	if err := proto.Unmarshal(finish, &message); err != nil {
		return nil, err
	}
	clientStatic, err := nh.Decrypt(message.GetClientFinish().GetStatic())
	if err != nil {
		return nil, err
	}
	if err := mix(ephemeral.Priv, clientStatic); err != nil {
		return nil, err
	}
	if _, err := nh.Decrypt(message.GetClientFinish().GetPayload()); err != nil {
		return nil, fmt.Errorf("client finish: %w", err)
	}
	kdf := hkdf.New(sha256.New, nil, salt, nil)
	readKey, writeKey := make([]byte, 32), make([]byte, 32)
	if _, err := io.ReadFull(kdf, readKey); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(kdf, writeKey); err != nil {
		return nil, err
	}
	makeCipher := func(key []byte) (cipher.AEAD, error) {
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	}
	peer := &bizNoise{conn: conn}
	if peer.readCipher, err = makeCipher(readKey); err != nil {
		return nil, err
	}
	peer.writeCipher, err = makeCipher(writeKey)
	return peer, err
}

func bizFrame(message []byte) ([]byte, error) {
	if len(message) < 3 {
		return nil, fmt.Errorf("short frame")
	}
	length := int(message[0])<<16 | int(message[1])<<8 | int(message[2])
	if length != len(message)-3 {
		return nil, fmt.Errorf("frame length %d != %d", length, len(message)-3)
	}
	return message[3:], nil
}

func bizWriteFrame(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	frame := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	return conn.Write(ctx, websocket.MessageBinary, append(frame, payload...))
}

func bizIV(count uint32) []byte {
	iv := make([]byte, 12)
	binary.BigEndian.PutUint32(iv[8:], count)
	return iv
}

func (p *bizNoise) read(ctx context.Context) (*waBinary.Node, error) {
	_, frame, err := p.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	frame, err = bizFrame(frame)
	if err != nil {
		return nil, err
	}
	plain, err := p.readCipher.Open(nil, bizIV(p.readCount), frame, nil)
	p.readCount++
	if err != nil {
		return nil, err
	}
	unpacked, err := waBinary.Unpack(plain)
	if err != nil {
		return nil, err
	}
	return waBinary.Unmarshal(unpacked)
}

func (p *bizNoise) write(ctx context.Context, node waBinary.Node) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	encoded, err := waBinary.Marshal(node)
	if err != nil {
		return err
	}
	ciphertext := p.writeCipher.Seal(nil, bizIV(p.writeCount), encoded, nil)
	p.writeCount++
	return bizWriteFrame(ctx, p.conn, ciphertext)
}
