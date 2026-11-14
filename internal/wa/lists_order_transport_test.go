package wa_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Real App.Sync fetches encrypted regular patches over certified Noise XX and
// WebSocket; the SDK verifies patch/snapshot MACs and LTHash before the production
// ordered owner writes SQL. The persisted order must survive restart and PN/LID
// reconciliation. An old wire patch is then rejected against the current hash.
// This proves neither pairing/login nor remote-service policy or phone UI.
// Never parallelize: the SDK certificate trust root is process-wide.
func TestListsAppliedOrderRealTransport(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "wacli")
	build := exec.Command("go", "build", "-tags", "sqlite_fts5", "-o", binary, "../../cmd/wacli")
	build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=1", "CGO_CFLAGS=-Wno-error=missing-braces")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual CLI: %v\n%s", err, output)
	}
	root := keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	serverPath := filepath.Join(t.TempDir(), "server.db")
	wa.PairedSessionStoreAt(t, serverPath)
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(serverPath, "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keyID, key := []byte{1, 2, 3, 4}, bytes.Repeat([]byte{0x52}, 32)
	putKey := func(d *waStore.Device) {
		t.Helper()
		if err := d.AppStateKeys.PutAppStateSyncKey(ctx, keyID, waStore.AppStateSyncKey{Data: key, Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
			t.Fatal(err)
		}
	}
	putKey(device)
	peer := &listsOrderPeer{t: t, processor: appstate.NewProcessor(device, waLog.Noop), keyID: keyID}
	pn := types.NewJID("5551000001", types.DefaultUserServer)
	lid := types.NewJID("5551000002", types.HiddenUserServer)
	definition := appstate.BuildLabelEdit("7", "Before", 3, false)
	rename := appstate.BuildLabelEdit("7", "Transport", 3, false)
	addPN := appstate.BuildLabelChat(pn, "7", true)
	addLID := appstate.BuildLabelChat(lid, "7", true)
	removeLID := appstate.BuildLabelChat(lid, "7", false)
	for i, patch := range []appstate.PatchInfo{definition, rename, addPN, addLID, removeLID} {
		// Additions tie across aliases; the later removal's clock runs backwards.
		patch.Timestamp = time.Unix([]int64{400, 200, 300, 300, 100}[i], 0)
		if err := peer.append(ctx, patch); err != nil {
			t.Fatal(err)
		}
		t.Logf("WIRE LOG prepared regular v%d action=%s device_timestamp=%d (MAC and LTHash verified)", i+1,
			[]string{"define Before", "rename Transport", "PN add", "LID add", "LID remove"}[i], patch.Timestamp.Unix())
	}
	first := openListsOrderTransport(t, ctx, dir, root, peer, putKey)
	if _, err := first.app.Sync(ctx, app.SyncOptions{Mode: app.SyncModeOnce, IdleExit: 20 * time.Millisecond, PresenceMode: app.SyncPresenceModeQuiet}); err != nil {
		t.Fatal(err)
	}
	first.rows("after real App.Sync, before alias reconciliation", pn, lid)
	first.expectMember(pn, true, "original PN addition")
	first.expectMember(lid, false, "later backwards-clock LID removal")
	lists, err := first.app.DB().Lists()
	if err != nil || len(lists.Lists) != 1 || lists.Lists[0].Name != "Transport" {
		t.Fatalf("backwards-clock definition rename: %+v, %v", lists, err)
	}
	if err := first.cli.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	t.Log("RESTART: save synthetic PN/LID mapping in test device store; close app, SQL and websocket")
	first.close()

	second := openListsOrderTransport(t, ctx, dir, root, peer, putKey)
	// App.Sync reconciles historical LIDs before fetching regular. Inspect this
	// actual persisted result before the peer returns a full historical replay.
	peer.mu.Lock()
	peer.beforeFetch = func() {
		second.rows("reopened store, App.Sync reconciliation before replay", pn, lid)
		second.expectMember(pn, false, "persisted applied_order must beat the older PN row's higher clock")
		var remaining int
		if err := second.sql.QueryRow(`SELECT count(*) FROM chat_lists WHERE chat_jid=?`, lid.String()).Scan(&remaining); err != nil || remaining != 0 {
			t.Errorf("reconciled LID membership rows=%d err=%v", remaining, err)
		}
	}
	peer.mu.Unlock()
	_, err = second.app.Sync(ctx, app.SyncOptions{Mode: app.SyncModeOnce, IdleExit: 20 * time.Millisecond, PresenceMode: app.SyncPresenceModeQuiet,
		AfterConnect: func(ctx context.Context) error {
			second.rows("after authenticated full replay on reopened store", pn, lid)
			second.expectMember(pn, false, "full replay retains accepted final removal")
			version, hash, err := second.cli.Store.AppState.GetAppStateVersion(ctx, "regular")
			if err != nil || version != peer.state.Version || hash != peer.state.Hash {
				return fmt.Errorf("SDK/server regular version or verified LTHash differs")
			}
			t.Logf("SDK/PEER agree at regular v%d with verified LTHash; no hash bytes or keys logged", version)
			peer.mu.Lock()
			peer.replayOld = true
			peer.mu.Unlock()
			// This is the real SDK fetch, during real App.Sync, not handler/event
			// injection. v3's MAC was valid for v3's original chain, not v5's.
			err = second.cli.FetchAppState(ctx, appstate.WAPatchRegular, false, false)
			if !errors.Is(err, appstate.ErrMismatchingLTHash) {
				return fmt.Errorf("historical v3 replay must fail LTHash validation: %w", err)
			}
			t.Log("WIRE REPLAY: authentic old v3 PN-add patch rejected against current v5 LTHash; App.Sync may recover by replaying the authenticated current chain")
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	second.rows("final durable state", pn, lid)
	second.expectMember(pn, false, "older wire replay and recovery cannot resurrect final membership")
	if order := second.order(pn); order < 6 {
		t.Fatalf("restart/replay lost persisted application sequence: %d", order)
	}
	second.close()
	for _, args := range [][]string{{"lists", "list"}, {"chats", "list", "--list", "7"}, {"chats", "list", "--list", "Transport"}} {
		command := exec.CommandContext(ctx, binary, append([]string{"--store", dir, "--read-only", "--json"}, args...)...)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		output := stdout.Bytes()
		if err != nil {
			t.Fatalf("real CLI %v: %v\n%s", args, err, output)
		}
		var result struct {
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(output, &result); err != nil || !result.Success {
			t.Fatalf("CLI output invalid: %s (%v)", output, err)
		}
		if args[0] == "chats" && strings.TrimSpace(string(result.Data)) != "[]" && strings.TrimSpace(string(result.Data)) != "null" {
			t.Fatalf("CLI filtering resurrected removed membership: %s", output)
		}
		t.Logf("REAL CLI %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
		if stderr.Len() > 0 {
			t.Logf("REAL CLI scope note: %s", strings.TrimSpace(stderr.String()))
		}
	}
	output, err := exec.CommandContext(ctx, binary, "--store", dir, "--read-only", "--json", "lists", "show", "7").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Available Commands:") || strings.Contains(string(output), `"success":`) {
		t.Fatalf("expected unsupported lists show to display group help, got %v: %s", err, output)
	}
	t.Log("REAL CLI lists show 7: displays group help, not JSON data; show is absent from this read-only PR and is not claimed as a supported read")
}

type listsOrderPeer struct {
	t           *testing.T
	mu          sync.Mutex
	processor   *appstate.Processor
	keyID       []byte
	state       appstate.HashState
	patches     [][]byte
	replayOld   bool
	beforeFetch func()
}

func (p *listsOrderPeer) append(ctx context.Context, info appstate.PatchInfo) error {
	raw, err := p.processor.EncodePatch(ctx, p.keyID, p.state, info)
	if err != nil {
		return err
	}
	var patch waServerSync.SyncdPatch
	if err := proto.Unmarshal(raw, &patch); err != nil {
		return err
	}
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(p.state.Version + 1)}
	_, state, err := p.processor.DecodePatches(ctx, &appstate.PatchList{Name: appstate.WAPatchRegular, Patches: []*waServerSync.SyncdPatch{&patch}}, p.state, true)
	if err != nil {
		return err
	}
	p.state = state
	raw, err = proto.Marshal(&patch)
	p.patches = append(p.patches, raw)
	return err
}

func (p *listsOrderPeer) response(node *waBinary.Node) waBinary.Node {
	response := waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "type": "result", "from": types.ServerJID}}
	collection, ok := node.GetOptionalChildByTag("sync", "collection")
	if !ok {
		return response
	}
	name := fmt.Sprint(collection.Attrs["name"])
	result := waBinary.Node{Tag: "collection", Attrs: waBinary.Attrs{"name": name, "has_more_patches": "false"}}
	if name == "regular" {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.beforeFetch != nil {
			p.beforeFetch()
			p.beforeFetch = nil
		}
		var patches []waBinary.Node
		if p.replayOld {
			patches = []waBinary.Node{{Tag: "patch", Content: p.patches[2]}}
			p.replayOld = false
			p.t.Log("WIRE IQ regular incremental fetch: peer returns historical v3 patch")
		} else {
			for _, raw := range p.patches {
				patches = append(patches, waBinary.Node{Tag: "patch", Content: raw})
			}
			p.t.Log("WIRE IQ regular full fetch: peer returns encrypted v1..v5 patches over Noise/WebSocket")
		}
		result.Content = []waBinary.Node{{Tag: "patches", Content: patches}}
	}
	response.Content = []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{result}}}
	return response
}

type listsOrderTransport struct {
	t     *testing.T
	app   *app.App
	cli   *whatsmeow.Client
	sql   *sql.DB
	close func()
}

func openListsOrderTransport(t *testing.T, ctx context.Context, dir string, root *keys.KeyPair, peer *listsOrderPeer, putKey func(*waStore.Device)) *listsOrderTransport {
	t.Helper()
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenWA(); err != nil {
		a.Close()
		t.Fatal(err)
	}
	cli := wa.RawClient(a.WA().(*wa.Client))
	cli.EnableAutoReconnect = false
	putKey(cli.Store)
	static := keys.NewKeyPair()
	cert := ownTransportCertificate(t, root, static)
	connections := make(chan *ownTransportNoise, 1)
	disconnected := make(chan struct{}, 1)
	handler := cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Disconnected); ok {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Error("test websocket accept failed")
			return
		}
		defer conn.CloseNow()
		noise, err := ownTransportHandshake(ctx, conn, static, cert)
		if err != nil {
			t.Errorf("test Noise handshake failed: %v", err)
			return
		}
		t.Log("NOISE XX: real SDK ConnectContext verified the synthetic CA; app-state key seeded only in test devices")
		connections <- noise
		for {
			node, err := noise.read(ctx)
			if err != nil {
				return
			}
			if node.Tag == "iq" {
				if err := noise.write(ctx, peer.response(node)); err != nil {
					return
				}
			}
		}
	}))
	cli.MessengerConfig = &whatsmeow.MessengerConfig{WebsocketURL: "ws" + strings.TrimPrefix(server.URL, "http"), BaseURL: server.URL}
	cli.SetWebsocketHTTPClient(server.Client())
	if err := cli.ConnectContext(ctx); err != nil {
		cli.RemoveEventHandler(handler)
		server.Close()
		a.Close()
		t.Fatal(err)
	}
	noise := <-connections
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "wacli.db"), "_busy_timeout=5000"))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = noise.conn.Close(websocket.StatusNormalClosure, "synthetic proof complete")
			select {
			case <-disconnected:
			case <-time.After(5 * time.Second):
				t.Error("SDK did not drain remote disconnect")
			}
			cli.RemoveEventHandler(handler)
			_ = db.Close()
			a.Close()
			server.Close()
		})
	}
	t.Cleanup(closeAll)
	return &listsOrderTransport{t: t, app: a, cli: cli, sql: db, close: closeAll}
}

func (e *listsOrderTransport) expectMember(jid types.JID, want bool, label string) {
	e.t.Helper()
	var got bool
	if err := e.sql.QueryRow(`SELECT labeled FROM chat_lists WHERE chat_jid=? AND list_id='7'`, jid.String()).Scan(&got); err != nil || got != want {
		e.t.Errorf("ORDER INVARIANT %s: membership=%t err=%v; want=%t", label, got, err, want)
	}
}

func (e *listsOrderTransport) order(jid types.JID) int64 {
	e.t.Helper()
	var order int64
	if err := e.sql.QueryRow(`SELECT applied_order FROM chat_lists WHERE chat_jid=? AND list_id='7'`, jid.String()).Scan(&order); err != nil {
		e.t.Error(err)
	}
	return order
}

func (e *listsOrderTransport) rows(label string, pn, lid types.JID) {
	e.t.Helper()
	rows, err := e.sql.Query(`SELECT chat_jid,labeled,updated_at,applied_order FROM chat_lists WHERE list_id='7' ORDER BY applied_order`)
	if err != nil {
		e.t.Error(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var jid string
		var labeled bool
		var at, order int64
		if err := rows.Scan(&jid, &labeled, &at, &order); err != nil {
			e.t.Error(err)
			return
		}
		alias := "unexpected"
		if jid == pn.String() {
			alias = "synthetic PN"
		} else if jid == lid.String() {
			alias = "synthetic LID"
		}
		e.t.Logf("STORE %s: alias=%s list=7 labeled=%t device_timestamp_ms=%d applied_order=%d", label, alias, labeled, at, order)
	}
	if err := rows.Err(); err != nil {
		e.t.Error(err)
	}
}
