package wa_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/sqliteutil"
	appStore "github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waCert"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/socket"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"google.golang.org/protobuf/proto"
)

// This is an app-layer transport proof, not phone or CLI/follow-owner proof.
// No exchange/fetch substitution: App -> WA -> sdkRegularAppStateSender ->
// SendIQ -> real NoiseSocket -> websocket -> syncd peer -> encrypted response.
// The peer performs Noise XX with a test CA (restored below), validates patches
// in its own SQL store, and serves a patch log, including full replay from zero.
// Pairing and login bootstrap are deliberately not emulated. The paired SQL
// device is seeded; ConnectContext performs the real certified handshake.
// Prior art: whatsmeow-409-race/appstate_409_race_test.go's syncd patch log.
func TestRegularRealTransport(t *testing.T) {
	// This test must stay nonparallel: the SDK exports a process-wide trust root.
	root := keys.NewKeyPair()
	previousRoot := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previousRoot })

	t.Run("ordered_edits_delete", func(t *testing.T) {
		e := newRegularTransport(t, root)
		e.captureOnly()
		q := e.create()
		for _, message := range []string{"A", "B"} {
			if _, err := e.app.EditQuickReply(e.ctx, q.ID, app.QuickReplyInput{Message: proto.String(message)}); err != nil {
				t.Fatal(err)
			}
			e.row(q.ID, message, false)
		}
		if _, err := e.app.DeleteQuickReply(e.ctx, q.ID); err != nil {
			t.Fatal(err)
		}
		e.row(q.ID, "B", true)
		e.checkServer([]transportRow{{"created", false}, {"A", false}, {"B", false}, {"B", true}}, 0)
		deleted := false
		for _, row := range e.audit() {
			deleted = deleted || row.Deleted
			if deleted && row != (transportRow{"B", true}) {
				t.Fatalf("row resurrected after deletion: %v", row)
			}
		}
	})

	t.Run("injected_409", func(t *testing.T) {
		e := newRegularTransport(t, root)
		e.captureOnly()
		q := e.create()
		// Inject only when SET arrives, after the app's pre-write fetch. This
		// makes the first send stale without replacing any client primitive.
		e.server.mu.Lock()
		e.server.foreign = transportReply(q.ID, "foreign", false)
		e.server.mu.Unlock()
		if _, err := e.app.EditQuickReply(e.ctx, q.ID, app.QuickReplyInput{Message: proto.String("retry wins")}); err != nil {
			t.Fatal(err)
		}
		e.row(q.ID, "retry wins", false)
		e.checkServer([]transportRow{{"created", false}, {"foreign", false}, {"retry wins", false}}, 1)
		rows := e.audit()
		want := []transportRow{{"retry wins", false}, {"foreign", false}, {"retry wins", false}}
		if len(rows) < 3 || !reflect.DeepEqual(rows[len(rows)-3:], want) {
			t.Fatalf("owner must persist optimistic value, conflict batch, retry batch: %v", rows)
		}
		t.Log("PASS: SDK applied v2 foreign then v3 retry; owner persisted conflict before retry")
	})

	t.Run("legacy_delayed_dispatch_control", func(t *testing.T) {
		e := newRegularTransport(t, root)
		entered, release := make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		// Gate an actual public SendAppState event BEFORE the app reserves its
		// persistence ticket. The SDK has already applied A to session.db.
		gate := e.cli.AddEventHandler(func(evt any) {
			if v, ok := evt.(*events.AppState); ok && v.GetQuickReplyAction().GetMessage() == "A" {
				t.Log("GATE: legacy A already applied in SDK; hold its detached callback before owner admission")
				close(entered)
				select {
				case <-release:
				case <-e.ctx.Done():
				}
			}
		})
		remove, err := e.app.AddChatStatePersistenceHandler(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		persisted := make(chan transportRow, 8)
		observer := e.cli.AddEventHandler(func(evt any) {
			if v, ok := evt.(*events.AppState); ok && v.GetQuickReplyAction() != nil {
				act := v.GetQuickReplyAction()
				persisted <- transportRow{act.GetMessage(), act.GetDeleted()}
			}
		})
		t.Cleanup(func() { unblock(); e.cli.RemoveEventHandler(observer); remove(); e.cli.RemoveEventHandler(gate) })
		wait := func(ch <-chan transportRow, want transportRow) {
			t.Helper()
			select {
			case got := <-ch:
				if got != want {
					t.Fatalf("callback=%v want %v", got, want)
				}
			case <-e.ctx.Done():
				t.Fatal("timed out waiting for persisted SDK callback")
			}
		}
		const id = "legacy-reply"
		for _, row := range []transportRow{{"created", false}, {"A", false}, {"B", false}, {"B", true}} {
			if err := e.cli.SendAppState(e.ctx, *transportReply(id, row.Message, row.Deleted)); err != nil {
				t.Fatal(err)
			}
			if row.Message == "A" {
				select {
				case <-entered:
				case <-e.ctx.Done():
					t.Fatal("legacy callback never entered gate")
				}
				continue
			}
			wait(persisted, row)
			e.row(id, row.Message, row.Deleted)
		}
		unblock()
		wait(persisted, transportRow{"A", false})
		e.row(id, "A", false)
		e.checkServer([]transportRow{{"created", false}, {"A", false}, {"B", false}, {"B", true}}, 0)
		e.audit()
		t.Log("EXPECTED CONTROL FAILURE: invariant latest=B/deleted=true is false; late A resurrected the row. New sender scenario above retains B/deleted=true.")
	})
}

type transportRow struct {
	Message string
	Deleted bool
}

type regularTransport struct {
	t      *testing.T
	ctx    context.Context
	app    *app.App
	cli    *whatsmeow.Client
	server *transportSyncd
	sql    *sql.DB
}

// Log SDK apply versions while delegating every operation to the real SQL store.
type transportApplyStore struct {
	waStore.AppStateStore
	t *testing.T
}

func (s transportApplyStore) PutAppStateVersion(ctx context.Context, name string, v uint64, hash [128]byte) error {
	err := s.AppStateStore.PutAppStateVersion(ctx, name, v, hash)
	s.t.Logf("SDK APPLY collection=%s version=%d SQL-error=%v", name, v, err)
	return err
}

func newRegularTransport(t *testing.T, root *keys.KeyPair) *regularTransport {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	cli := wa.RawClient(a.WA().(*wa.Client))
	cli.EnableAutoReconnect = false
	cli.Store.AppState = transportApplyStore{AppStateStore: cli.Store.AppState, t: t}
	id, key := []byte{1, 2, 3, 4}, bytes.Repeat([]byte{0x51}, 32)
	putKey := func(device *waStore.Device) {
		t.Helper()
		if err := device.AppStateKeys.PutAppStateSyncKey(ctx, id, waStore.AppStateSyncKey{Data: key, Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
			t.Fatal(err)
		}
	}
	putKey(cli.Store)
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
	putKey(device)
	s := &transportSyncd{t: t, processor: appstate.NewProcessor(device, waLog.Noop), keyID: id}
	static := keys.NewKeyPair()
	cert := transportCertificate(t, root, static)
	serverConn := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("websocket accept: %v", err)
			return
		}
		defer conn.CloseNow()
		serverConn <- conn
		peer, err := transportHandshake(ctx, conn, static, cert)
		if err != nil {
			t.Errorf("Noise server: %v", err)
			return
		}
		t.Log("NOISE XX: real ConnectContext, test-CA certificate verified; seeded paired device, no account login bootstrap")
		for {
			node, err := peer.read(ctx)
			if err != nil {
				return // Client disconnect/cleanup closes the websocket.
			}
			response, err := s.iq(ctx, node)
			if err != nil {
				t.Errorf("syncd: %v", err)
				return
			}
			if err := peer.write(ctx, response); err != nil {
				t.Errorf("syncd response: %v", err)
				return
			}
		}
	}))
	t.Cleanup(httpServer.Close)
	// URL override is exported; unlike a socket injection this exercises the
	// client's actual ConnectContext/DoHandshake/MakeFrameHandler path.
	cli.MessengerConfig = &whatsmeow.MessengerConfig{WebsocketURL: "ws" + strings.TrimPrefix(httpServer.URL, "http"), BaseURL: httpServer.URL}
	cli.SetWebsocketHTTPClient(httpServer.Client())
	disconnected := make(chan struct{})
	disconnectHandler := cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Disconnected); ok {
			close(disconnected)
		}
	})
	if err := cli.ConnectContext(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Drain the remote disconnect before manual cleanup. The pinned SDK's
		// manual NoiseSocket.Stop can otherwise race its read-pump Close over
		// FrameSocket.OnDisconnect; this harness need not exercise that race.
		t.Log("CLEANUP: peer closes the test websocket after assertions")
		_ = (<-serverConn).Close(websocket.StatusNormalClosure, "test complete")
		select {
		case <-disconnected:
		case <-time.After(5 * time.Second):
			t.Error("SDK did not drain remote disconnect")
		}
		cli.RemoveEventHandler(disconnectHandler)
		cli.Disconnect()
	})
	if !cli.IsConnected() {
		t.Fatal("real NoiseSocket is not connected")
	}
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "wacli.db"), "_busy_timeout=5000"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Observe actual owner writes without replacing storage or persistence.
	if _, err := db.Exec(`CREATE TABLE transport_audit(seq INTEGER PRIMARY KEY, message TEXT, deleted INTEGER);
		CREATE TRIGGER transport_insert AFTER INSERT ON quick_replies BEGIN
		INSERT INTO transport_audit(message,deleted) VALUES(new.message,new.deleted); END;
		CREATE TRIGGER transport_update AFTER UPDATE ON quick_replies BEGIN
		INSERT INTO transport_audit(message,deleted) VALUES(new.message,new.deleted); END;`); err != nil {
		t.Fatal(err)
	}
	return &regularTransport{t: t, ctx: ctx, app: a, cli: cli, server: s, sql: db}
}

func (e *regularTransport) create() appStore.QuickReply {
	e.t.Helper()
	q, err := e.app.CreateQuickReply(e.ctx, app.QuickReplyInput{Shortcut: proto.String("transport"), Message: proto.String("created")})
	if err != nil {
		e.t.Fatal(err)
	}
	e.row(q.ID, "created", false)
	return q
}

func (e *regularTransport) captureOnly() {
	e.t.Helper()
	remove, err := e.app.AddChatStatePersistenceHandler(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(remove)
	id := e.cli.AddEventHandler(func(evt any) {
		if v, ok := evt.(*events.AppState); ok && v.GetQuickReplyAction() != nil {
			e.t.Errorf("new regular sender leaked a detached quick-reply callback: %q", v.GetQuickReplyAction().GetMessage())
		}
	})
	e.t.Cleanup(func() { e.cli.RemoveEventHandler(id) })
}

func (e *regularTransport) row(id, message string, deleted bool) {
	e.t.Helper()
	q, found, err := e.app.DB().QuickReplyByID(id)
	if err != nil || !found || q.Message != message || q.Deleted != deleted {
		e.t.Fatalf("persisted=%+v found=%t err=%v; want %q deleted=%t", q, found, err, message, deleted)
	}
	e.t.Logf("PERSISTED reply message=%q deleted=%t", q.Message, q.Deleted)
}

func (e *regularTransport) audit() []transportRow {
	e.t.Helper()
	rows, err := e.sql.Query(`SELECT seq,message,deleted FROM transport_audit ORDER BY seq`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var result []transportRow
	for rows.Next() {
		var seq int
		var row transportRow
		if err := rows.Scan(&seq, &row.Message, &row.Deleted); err != nil {
			e.t.Fatal(err)
		}
		e.t.Logf("OWNER SQL seq=%d message=%q deleted=%t", seq, row.Message, row.Deleted)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return result
}

func (e *regularTransport) checkServer(want []transportRow, conflicts int) {
	e.t.Helper()
	e.server.mu.Lock()
	defer e.server.mu.Unlock()
	if !reflect.DeepEqual(e.server.accepted, want) || e.server.conflicts != conflicts {
		e.t.Fatalf("server patches=%v conflicts=%d; want %v/%d", e.server.accepted, e.server.conflicts, want, conflicts)
	}
	v, hash, err := e.cli.Store.AppState.GetAppStateVersion(e.ctx, "regular")
	if err != nil || v != e.server.state.Version || hash != e.server.state.Hash {
		e.t.Fatalf("SDK/server hash/version differ: SDK=%d server=%d err=%v", v, e.server.state.Version, err)
	}
	e.t.Logf("SERVER/SDK agree: regular v%d, verified LTHash; conflicts=%d", v, conflicts)
}

func transportReply(id, message string, deleted bool) *appstate.PatchInfo {
	return &appstate.PatchInfo{Type: appstate.WAPatchRegular, Timestamp: time.Unix(1700000000, 0), Mutations: []appstate.MutationInfo{{
		Index: []string{appstate.IndexQuickReply, id}, Version: 3,
		Value: &waSyncAction.SyncActionValue{QuickReplyAction: &waSyncAction.QuickReplyAction{Shortcut: proto.String("transport"), Message: proto.String(message), Deleted: proto.Bool(deleted)}},
	}}}
}

type transportSyncd struct {
	t         *testing.T
	mu        sync.Mutex
	processor *appstate.Processor
	keyID     []byte
	state     appstate.HashState
	patches   [][]byte
	accepted  []transportRow
	foreign   *appstate.PatchInfo
	conflicts int
}

func (s *transportSyncd) append(ctx context.Context, raw []byte) error {
	var patch waServerSync.SyncdPatch
	if err := proto.Unmarshal(raw, &patch); err != nil {
		return err
	}
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(s.state.Version + 1)}
	mutations, state, err := s.processor.DecodePatches(ctx, &appstate.PatchList{Name: appstate.WAPatchRegular, Patches: []*waServerSync.SyncdPatch{&patch}}, s.state, true)
	if err != nil {
		return fmt.Errorf("verify client patch: %w", err)
	}
	if len(mutations) != 1 || mutations[0].Action.GetQuickReplyAction() == nil || mutations[0].Version != 3 {
		return fmt.Errorf("expected one version-3 reply mutation")
	}
	act := mutations[0].Action.GetQuickReplyAction()
	s.accepted = append(s.accepted, transportRow{act.GetMessage(), act.GetDeleted()})
	s.state = state
	encoded, err := proto.Marshal(&patch)
	if err != nil {
		return err
	}
	s.patches = append(s.patches, encoded)
	s.t.Logf("SERVER ACCEPT v%d message=%q deleted=%t (MAC verified)", state.Version, act.GetMessage(), act.GetDeleted())
	return nil
}

func (s *transportSyncd) since(version uint64) []waBinary.Node {
	var nodes []waBinary.Node
	for v := version; v < uint64(len(s.patches)); v++ {
		nodes = append(nodes, waBinary.Node{Tag: "patch", Content: s.patches[v]})
	}
	return nodes
}

func (s *transportSyncd) iq(ctx context.Context, iq *waBinary.Node) (waBinary.Node, error) {
	response := waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": iq.Attrs["id"], "type": "result", "from": types.ServerJID}}
	if iq.Tag != "iq" || iq.Attrs["xmlns"] != "w:sync:app:state" {
		return response, fmt.Errorf("unexpected non-syncd request: %s", iq.Tag)
	}
	collection, ok := iq.GetOptionalChildByTag("sync", "collection")
	if !ok {
		return response, fmt.Errorf("missing collection")
	}
	ag := collection.AttrGetter()
	// Snapshot requests omit version rather than encoding zero.
	version, _ := ag.GetUint64("version", false)
	if ag.String("name") != "regular" || ag.Error() != nil {
		return response, fmt.Errorf("bad collection: %v", ag.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := waBinary.Node{Tag: "collection", Attrs: waBinary.Attrs{"name": "regular", "has_more_patches": "false"}}
	if patch, send := collection.GetOptionalChildByTag("patch"); send {
		s.t.Logf("IQ OUT %v/write regular base=v%d", iq.Attrs["type"], version)
		if s.foreign != nil {
			raw, err := s.processor.EncodePatch(ctx, s.keyID, s.state, *s.foreign)
			if err != nil {
				return response, err
			}
			if err := s.append(ctx, raw); err != nil {
				return response, err
			}
			s.foreign = nil
		}
		if version != s.state.Version {
			s.conflicts++
			result.Attrs["type"] = "error"
			result.Content = []waBinary.Node{{Tag: "error", Attrs: waBinary.Attrs{"code": "409"}}, {Tag: "patches", Content: s.since(version)}}
			s.t.Logf("IQ IN 409 regular pending=v%d..v%d", version+1, s.state.Version)
		} else {
			raw, ok := patch.Content.([]byte)
			if !ok {
				return response, fmt.Errorf("patch is not bytes")
			}
			if err := s.append(ctx, raw); err != nil {
				return response, err
			}
			result.Attrs["version"] = fmt.Sprint(s.state.Version)
			s.t.Logf("IQ IN accepted regular v%d", s.state.Version)
		}
	} else {
		s.t.Logf("IQ OUT %v/fetch regular from=v%d snapshot=%t", iq.Attrs["type"], version, ag.OptionalBool("return_snapshot"))
		result.Content = []waBinary.Node{{Tag: "patches", Content: s.since(version)}}
		s.t.Logf("IQ IN patches count=%d end=v%d", len(s.since(version)), s.state.Version)
	}
	response.Content = []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{result}}}
	return response, nil
}

func transportCertificate(t *testing.T, root, static *keys.KeyPair) []byte {
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

// The peer mirrors the SDK's Noise XX transcript. Its final directions are
// reversed: client write = server read. Both IQ directions traverse encrypted
// websocket frames; no ReceiveResponse or direct handler injection is used.
type transportNoise struct {
	conn        *websocket.Conn
	readCipher  cipher.AEAD
	writeCipher cipher.AEAD
	readCount   uint32
	writeCount  uint32
}

func transportHandshake(ctx context.Context, conn *websocket.Conn, static *keys.KeyPair, cert []byte) (*transportNoise, error) {
	_, hello, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(hello, socket.WAConnHeader) {
		return nil, fmt.Errorf("missing WA header")
	}
	hello, err = transportFrame(hello[len(socket.WAConnHeader):])
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
	// Track the chaining key to derive the responder's reversed final keys.
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
	if err := transportWriteFrame(ctx, conn, response); err != nil {
		return nil, err
	}
	_, finish, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	finish, err = transportFrame(finish)
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
	peer := &transportNoise{conn: conn}
	if peer.readCipher, err = makeCipher(readKey); err != nil {
		return nil, err
	}
	peer.writeCipher, err = makeCipher(writeKey)
	return peer, err
}

func transportFrame(message []byte) ([]byte, error) {
	if len(message) < 3 {
		return nil, fmt.Errorf("short frame")
	}
	length := int(message[0])<<16 | int(message[1])<<8 | int(message[2])
	if length != len(message)-3 {
		return nil, fmt.Errorf("frame length %d != %d", length, len(message)-3)
	}
	return message[3:], nil
}

func transportWriteFrame(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	frame := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	return conn.Write(ctx, websocket.MessageBinary, append(frame, payload...))
}

func transportIV(count uint32) []byte {
	iv := make([]byte, 12)
	binary.BigEndian.PutUint32(iv[8:], count)
	return iv
}

func (p *transportNoise) read(ctx context.Context) (*waBinary.Node, error) {
	_, frame, err := p.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	frame, err = transportFrame(frame)
	if err != nil {
		return nil, err
	}
	plain, err := p.readCipher.Open(nil, transportIV(p.readCount), frame, nil)
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

func (p *transportNoise) write(ctx context.Context, node waBinary.Node) error {
	encoded, err := waBinary.Marshal(node)
	if err != nil {
		return err
	}
	// Marshal already includes the uncompressed-frame flag.
	ciphertext := p.writeCipher.Seal(nil, transportIV(p.writeCount), encoded, nil)
	p.writeCount++
	return transportWriteFrame(ctx, p.conn, ciphertext)
}
