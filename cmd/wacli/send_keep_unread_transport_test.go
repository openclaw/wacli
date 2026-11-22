package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/util/optional"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCert"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/socket"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"google.golang.org/protobuf/proto"
)

// Real Cobra -> real direct App / locked-store IPC -> follow App.Sync ->
// whatsmeow -> Signal -> Noise XX/WebSocket -> local peer -> ack -> SQLite.
// This lives in cmd/wacli to exercise the actual command, not reconstructed
// request objects. The Noise responder below is copied from
// internal/wa/own_message_transport_test.go: test-only exports cannot be
// imported by this package. No WAClient, event, auth, opener or send is faked.
// Only the HTTPS dial is redirected to the peer, which serves synthetic device
// lists, LID mappings and real signed Signal bundles. Never run in parallel:
// both the SDK trust root and HTTP default transport are process-wide.
// This does not prove pairing/login, remote-service policy or the phone UI.
func TestSendKeepUnreadRealTransport(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		for _, keep := range []bool{true, false} {
			for _, marker := range []bool{false, true} {
				t.Run(fmt.Sprintf("delegated=%v/keep=%v/manual=%v", delegated, keep, marker), func(t *testing.T) {
					f := newKeepTransport(t)
					flags := &rootFlags{storeDir: f.dir, asJSON: true, timeout: 10 * time.Second}
					// Both counted inputs must be decrypted and persisted by Sync.
					a, peer, stop := f.follow(flags, false, false)
					f.deliver(a, peer, f.incoming("incoming-one", f.chat, f.base))
					f.state(a.DB(), "after first wire input", true, 1)
					f.deliver(a, peer, f.incoming("incoming-two", f.chat, f.base.Add(time.Second)))
					f.state(a.DB(), "after second wire input", true, 2)
					count := 2
					if marker {
						if err := a.DB().SetChatUnreadCount(f.chat.String(), 0); err != nil {
							t.Fatal(err)
						}
						if err := a.DB().SetChatUnread(f.chat.String(), true); err != nil {
							t.Fatal(err)
						}
						count = 0
						f.state(a.DB(), "manual unread marker", true, 0)
					}
					stop()

					var owner *app.App
					var ownerPeer *keepTransportNoise
					var stopOwner func()
					if delegated {
						owner, ownerPeer, stopOwner = f.follow(flags, true, false)
						defer stopOwner()
						t.Log("COMMAND delegated: real follow owner holds LOCK; Cobra must use IPC")
					} else {
						t.Log("COMMAND direct: no sync owner or IPC listener; Cobra opens, locks, authenticates and connects")
					}
					args := []string{"--to", f.chat.String(), "--message", "synthetic automatic reply", "--no-preview", "--post-send-wait", "0"}
					if keep {
						args = append(args, "--keep-unread")
					}
					cmd := newSendTextCmd(flags)
					cmd.SetArgs(args)
					output := captureRootStdout(t, func() {
						if err := cmd.Execute(); err != nil {
							t.Fatal(err)
						}
					})
					var result struct {
						Success bool `json:"success"`
						Data    struct {
							Sent         bool   `json:"sent"`
							ID           string `json:"id"`
							StoreWarning string `json:"store_warning"`
						} `json:"data"`
					}
					if err := json.Unmarshal([]byte(output), &result); err != nil || !result.Success || !result.Data.Sent || result.Data.ID == "" || result.Data.StoreWarning != "" {
						t.Fatalf("Cobra did not report accepted and persisted send: %v", err)
					}
					sent := f.takeSent()
					if sent != result.Data.ID {
						t.Fatal("Cobra accepted ID differs from actual encrypted wire message")
					}
					t.Log("COBRA sent=true; output ID equals encrypted message ID and peer ack ID (IDs redacted)")
					unread, wantCount := keep, count
					if !keep {
						wantCount = 0
						t.Log("CONTROL no flag: v0.26.0 local acknowledgement must clear unread")
					}
					if delegated {
						f.state(owner.DB(), "after delegated send", unread, wantCount)
						f.assertStored(owner.DB(), sent)
						f.deliver(owner, ownerPeer, f.own(sent, f.chat, time.Now().UTC()))
						f.state(owner.DB(), "after own-device encrypted echo", unread, wantCount)
						stopOwner()
					} else {
						// Direct Cobra closes its App on return. Read the actual file,
						// then start Sync solely to receive the own-device echo.
						db, err := store.Open(filepath.Join(f.dir, "wacli.db"))
						if err != nil {
							t.Fatal(err)
						}
						f.state(db, "after direct send (command App closed)", unread, wantCount)
						f.assertStored(db, sent)
						_ = db.Close()
						p := f.takePeer()
						f.waitClosed(p)
						echoApp, echoPeer, stopEcho := f.follow(flags, false, false)
						f.deliver(echoApp, echoPeer, f.own(sent, f.chat, time.Now().UTC()))
						f.state(echoApp.DB(), "after own-device encrypted echo", unread, wantCount)
						stopEcho()
					}
					reopened, restartPeer, stopRestart := f.follow(flags, false, false)
					f.state(reopened.DB(), "reopened store before replay", unread, wantCount)
					f.deliver(reopened, restartPeer, f.own(sent, f.chat, time.Now().UTC()))
					f.state(reopened.DB(), "same ID replay over new socket after restart", unread, wantCount)
					f.assertStored(reopened.DB(), sent)
					stopRestart()
					f.wireAssertions(1)
					if f.deviceQueries.Load() == 0 || f.bundles.Load() == 0 {
						t.Fatal("send bypassed real device discovery or Signal bundle fetch")
					}
				})
			}
		}
	}
	t.Run("stable-v1-owner-upgrade", func(t *testing.T) {
		f := newKeepTransport(t)
		flags := &rootFlags{storeDir: f.dir, asJSON: true, timeout: 10 * time.Second}
		a, peer, stop := f.follow(flags, true, true)
		defer stop()
		f.deliver(a, peer, f.incoming("stable-input-one", f.chat, f.base))
		f.deliver(a, peer, f.incoming("stable-input-two", f.chat, f.base.Add(time.Second)))
		f.state(a.DB(), "before v1-owner request", true, 2)
		cmd := newSendTextCmd(flags)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		cmd.SetArgs([]string{"--to", f.chat.String(), "--message", "synthetic automatic reply", "--keep-unread", "--no-preview", "--post-send-wait", "0"})
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "upgrade wacli and restart sync --follow") || !strings.Contains(err.Error(), "not sent") {
			t.Fatalf("missing fail-closed upgrade explanation: %v", err)
		}
		writeRootError(*flags, err)
		t.Logf("COBRA error: %v", err)
		if f.ipcRequests.Load() != 1 || f.dispatches.Load() != 0 {
			t.Fatal("v1 owner must reject before dispatch, without a policy-free retry")
		}
		f.state(a.DB(), "after refused request (no fallback/retry)", true, 2)
		stop()
		f.wireAssertions(0)
		t.Log("STABLE OWNER: v0.26.0-equivalent v1 guard on real IPC listener; one v2 request, zero dispatches, zero wire messages")
	})
}

type keepTransport struct {
	t                                                       *testing.T
	ctx                                                     context.Context
	dir                                                     string
	chat, lid                                               types.JID
	base                                                    time.Time
	self, recipient                                         *waStore.Device
	peers                                                   chan *keepTransportNoise
	receipts                                                chan string
	sent                                                    chan string
	incoming, own                                           func(string, types.JID, time.Time) waBinary.Node
	messages, readReceipts, patches, deviceQueries, bundles atomic.Int32
	ipcRequests, dispatches                                 atomic.Int32
}

func newKeepTransport(t *testing.T) *keepTransport {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	t.Cleanup(cancel)
	f := &keepTransport{t: t, ctx: ctx, dir: t.TempDir(), chat: types.NewJID("5551230000", types.DefaultUserServer),
		lid: types.NewJID("555000002", types.HiddenUserServer), base: time.Unix(1700000000, 0),
		peers: make(chan *keepTransportNoise, 16), receipts: make(chan string, 32), sent: make(chan string, 16)}
	f.self = f.device(filepath.Join(f.dir, "session.db"), types.JID{User: "5550000000", Device: 7, Server: types.DefaultUserServer})
	f.self.LID = types.JID{User: "555000001", Device: 7, Server: types.HiddenUserServer}
	f.self.PushName = "Synthetic Proof"
	if err := f.self.Save(ctx); err != nil {
		t.Fatal(err)
	}
	// Suppress only unnecessary key upload by provisioning real local keys.
	if _, err := f.self.PreKeys.GetOrGenPreKeys(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if err := f.self.PreKeys.MarkPreKeysAsUploaded(ctx, 5); err != nil {
		t.Fatal(err)
	}
	to := f.lid
	to.Device = 2 // Distinct from incoming device zero: outbound must fetch keys.
	f.recipient = f.device(filepath.Join(t.TempDir(), "recipient.db"), to)
	f.incoming = f.sender(f.chat)
	own := f.self.GetLID()
	own.Device = 1
	f.own = f.sender(own)

	root, static := keys.NewKeyPair(), keys.NewKeyPair()
	previousRoot := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previousRoot })
	cert := keepTransportCertificate(t, root, static)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		peer, err := keepTransportHandshake(ctx, conn, static, cert)
		if err != nil {
			t.Errorf("Noise XX handshake failed: %v", err)
			return
		}
		peer.done = make(chan struct{})
		defer close(peer.done)
		t.Log("NOISE XX: real SDK verified synthetic test-CA certificate; WebSocket connected")
		f.peers <- peer
		// Exercise the real SDK's authenticated-session event, not an injected
		// Connected event. The account is already synthetic/pre-paired.
		if err := peer.write(ctx, waBinary.Node{Tag: "success", Attrs: waBinary.Attrs{"lid": f.self.GetLID(), "t": time.Now().Unix()}}); err != nil {
			t.Error(err)
			return
		}
		for {
			node, err := peer.read(ctx)
			if err != nil {
				return
			}
			t.Logf("PEER received %s", keepTransportShape(*node))
			if err := f.respond(peer, node); err != nil {
				t.Errorf("peer response: %v", err)
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	// The SDK clones http.DefaultTransport at construction. Redirect only its
	// WhatsApp TLS dial; every other dial fails closed. This lets unmodified
	// Cobra/newApp open the real client without exporting a production hook.
	previousTransport := http.DefaultTransport
	transport := previousTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("offline proof forbids non-TLS external dial")
	}
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "web.whatsapp.com:443" {
			return nil, fmt.Errorf("offline proof forbids other external TLS dial")
		}
		dialer := tls.Dialer{Config: server.Client().Transport.(*http.Transport).TLSClientConfig}
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { transport.CloseIdleConnections(); http.DefaultTransport = previousTransport })
	return f
}

func (f *keepTransport) device(path string, jid types.JID) *waStore.Device {
	f.t.Helper()
	container, err := sqlstore.New(f.ctx, "sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on"), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = container.Close() })
	d := container.NewDevice()
	d.ID = &jid
	d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := d.Save(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *keepTransport) follow(flags *rootFlags, delegated, stable bool) (*app.App, *keepTransportNoise, func()) {
	f.t.Helper()
	a, lk, err := newApp(f.ctx, flags, true, false)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := a.OpenWA(); err != nil {
		closeApp(a, lk)
		f.t.Fatal(err)
	}
	a.WA().SetAutoReconnect(false)
	disconnected := make(chan struct{}, 1)
	observer := a.WA().AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Disconnected); ok {
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}
	})
	ctx, cancel := context.WithCancel(f.ctx)
	ready, done := make(chan struct{}), make(chan error, 1)
	var stopIPC func()
	go func() {
		_, err := a.Sync(ctx, app.SyncOptions{Mode: app.SyncModeFollow, PresenceMode: app.SyncPresenceModeQuiet, AfterConnect: func(ctx context.Context) error {
			if delegated {
				stop, err := startSendDelegateServerForStore(ctx, a.StoreDir(), sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
					f.ipcRequests.Add(1)
					f.t.Logf("IPC decoded version=%d keep_unread=%v kind=%s", req.Version, req.KeepUnread, req.Kind)
					if req.KeepUnread && req.Version != sendKeepUnreadDelegateVersion {
						f.t.Error("keep-unread crossed IPC without its required v2 envelope")
					}
					if stable && req.Version != 1 {
						// Exact pre-dispatch version guard from stable v0.26.0.
						return sendDelegateResponse{}, fmt.Errorf("unsupported send delegate version %d", req.Version)
					}
					f.dispatches.Add(1)
					return executeDelegatedSend(ctx, a, req)
				})
				if err != nil {
					return err
				}
				stopIPC = stop
			}
			close(ready)
			return nil
		}})
		done <- err
	}()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		closeApp(a, lk)
		f.t.Fatalf("follow failed before ready: %v", err)
	case <-f.ctx.Done():
		cancel()
		closeApp(a, lk)
		f.t.Fatal("follow did not become ready")
	}
	p := f.takePeer()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if stopIPC != nil {
				stopIPC()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				f.t.Error("follow did not stop")
			}
			_ = p.conn.Close(websocket.StatusNormalClosure, "proof complete")
			select {
			case <-disconnected:
			case <-time.After(5 * time.Second):
				f.t.Error("SDK disconnect not drained")
			}
			a.WA().RemoveEventHandler(observer)
			closeApp(a, lk)
			f.waitClosed(p)
		})
	}
	f.t.Cleanup(stop)
	return a, p, stop
}

func (f *keepTransport) takePeer() *keepTransportNoise {
	f.t.Helper()
	select {
	case p := <-f.peers:
		return p
	case <-f.ctx.Done():
		f.t.Fatal("no peer connection")
	}
	return nil
}

func (f *keepTransport) waitClosed(p *keepTransportNoise) {
	f.t.Helper()
	select {
	case <-p.done:
	case <-f.ctx.Done():
		f.t.Error("peer socket did not close")
	}
}

func (f *keepTransport) takeSent() string {
	f.t.Helper()
	select {
	case id := <-f.sent:
		return id
	case <-f.ctx.Done():
		f.t.Fatal("no encrypted outbound message")
	}
	return ""
}

func (f *keepTransport) deliver(a *app.App, p *keepTransportNoise, node waBinary.Node) {
	f.t.Helper()
	id := fmt.Sprint(node.Attrs["id"])
	seen := make(chan string, 1)
	handler := a.WA().AddEventHandler(func(evt any) {
		if msg, ok := evt.(*events.Message); ok && msg.Info.ID == id {
			seen <- id
		}
	})
	defer a.WA().RemoveEventHandler(handler)
	if err := p.write(f.ctx, node); err != nil {
		f.t.Fatal(err)
	}
	for _, ch := range []<-chan string{seen, f.receipts} {
		select {
		case got := <-ch:
			if got != id {
				f.t.Fatal("event/receipt ID mismatch")
			}
		case <-f.ctx.Done():
			f.t.Fatal("no decrypted event or delivery receipt")
		}
	}
	f.t.Log("SOCKET input decrypted, App.Sync persistence completed and delivery/sender receipt acknowledged")
}

func (f *keepTransport) state(db *store.DB, label string, unread bool, count int) {
	f.t.Helper()
	c, err := db.GetChat(f.chat.String())
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Logf("STORE %s: unread=%v unread_count=%d", label, c.Unread, c.UnreadCount)
	if c.Unread != unread || c.UnreadCount != count {
		f.t.Errorf("%s: lost unread policy: got (%v,%d), want (%v,%d)", label, c.Unread, c.UnreadCount, unread, count)
	}
}

func (f *keepTransport) assertStored(db *store.DB, id string) {
	f.t.Helper()
	row, err := db.GetMessage(f.chat.String(), id)
	if err != nil || !row.FromMe {
		f.t.Fatalf("accepted outbound row missing at canonical phone chat: %v", err)
	}
	f.t.Log("STORE accepted outbound row exists at canonical phone chat; from_me=true")
}

func (f *keepTransport) wireAssertions(want int32) {
	f.t.Helper()
	f.t.Logf("WIRE totals: encrypted_messages=%d read_receipts=%d app_state_patches=%d device_queries=%d prekey_bundles=%d",
		f.messages.Load(), f.readReceipts.Load(), f.patches.Load(), f.deviceQueries.Load(), f.bundles.Load())
	if f.messages.Load() != want || f.readReceipts.Load() != 0 || f.patches.Load() != 0 {
		f.t.Error("wire message/read-receipt/app-state policy mismatch")
	}
}

// Log every stanza, including nested tags and protocol types, but no key bytes,
// ciphertexts, endpoints, numeric JIDs or random message/request IDs.
func keepTransportShape(n waBinary.Node) string {
	s := "<" + n.Tag
	for _, key := range []string{"type", "xmlns", "class", "context", "name"} {
		if val, ok := n.Attrs[key]; ok {
			s += fmt.Sprintf(" %s=%q", key, val)
		}
	}
	s += ">"
	for _, child := range n.GetChildren() {
		s += keepTransportShape(child)
	}
	return s + "</" + n.Tag + ">"
}

func (f *keepTransport) respond(p *keepTransportNoise, n *waBinary.Node) error {
	switch n.Tag {
	case "receipt":
		kind := fmt.Sprint(n.Attrs["type"])
		if kind != "inactive" && kind != "sender" {
			f.readReceipts.Add(1)
		}
		if err := p.write(f.ctx, waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": n.Attrs["id"], "class": "receipt", "type": n.Attrs["type"], "from": n.Attrs["to"]}}); err != nil {
			return err
		}
		f.receipts <- fmt.Sprint(n.Attrs["id"])
	case "message":
		if err := f.decryptOutbound(n); err != nil {
			return err
		}
		f.messages.Add(1)
		f.sent <- fmt.Sprint(n.Attrs["id"])
		f.t.Log("PEER accepted real encrypted message; Signal decrypt and synthetic text match; returning message ack")
		return p.write(f.ctx, waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": n.Attrs["id"], "class": "message", "from": n.Attrs["to"], "t": time.Now().Unix()}})
	case "iq":
		response := waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": n.Attrs["id"], "type": "result", "from": types.ServerJID}}
		if usync, ok := n.GetOptionalChildByTag("usync"); ok {
			var users []waBinary.Node
			_, devices := usync.GetOptionalChildByTag("query", "devices")
			if devices {
				f.deviceQueries.Add(1)
			}
			listNode := usync.GetChildByTag("list")
			for _, user := range listNode.GetChildren() {
				jid, _ := user.Attrs["jid"].(types.JID)
				if jid.IsEmpty() {
					jid = f.chat
				} // registration phone query
				children := []waBinary.Node{{Tag: "contact", Attrs: waBinary.Attrs{"type": "in"}}}
				if jid == f.chat {
					children = append(children, waBinary.Node{Tag: "lid", Attrs: waBinary.Attrs{"val": f.lid}})
				}
				if devices {
					var list []waBinary.Node
					if jid.User == f.chat.User || jid.User == f.lid.User {
						list = []waBinary.Node{{Tag: "device", Attrs: waBinary.Attrs{"id": "2"}}}
					}
					children = append(children, waBinary.Node{Tag: "devices", Content: []waBinary.Node{{Tag: "device-list", Content: list}}})
				}
				users = append(users, waBinary.Node{Tag: "user", Attrs: waBinary.Attrs{"jid": jid}, Content: children})
			}
			response.Content = []waBinary.Node{{Tag: "usync", Content: []waBinary.Node{{Tag: "list", Content: users}}}}
			f.t.Log("PEER served synthetic registration/LID identity and device list over usync")
		} else if key, ok := n.GetOptionalChildByTag("key"); ok && n.Attrs["xmlns"] == "encrypt" {
			var users []waBinary.Node
			for _, user := range key.GetChildren() {
				if user.Attrs["jid"] != *f.recipient.ID {
					return fmt.Errorf("unexpected recipient in bundle query")
				}
				var reg, signedID [4]byte
				binary.BigEndian.PutUint32(reg[:], f.recipient.RegistrationID)
				binary.BigEndian.PutUint32(signedID[:], f.recipient.SignedPreKey.KeyID)
				users = append(users, waBinary.Node{Tag: "user", Attrs: waBinary.Attrs{"jid": *f.recipient.ID}, Content: []waBinary.Node{
					{Tag: "registration", Content: reg[:]}, {Tag: "identity", Content: f.recipient.IdentityKey.Pub[:]},
					{Tag: "skey", Content: []waBinary.Node{{Tag: "id", Content: signedID[1:]}, {Tag: "value", Content: f.recipient.SignedPreKey.Pub[:]}, {Tag: "signature", Content: f.recipient.SignedPreKey.Signature[:]}}},
				}})
				f.bundles.Add(1)
			}
			response.Content = []waBinary.Node{{Tag: "list", Content: users}}
			f.t.Log("PEER served real signed Signal prekey bundle for synthetic recipient device")
		} else if _, ok := n.GetOptionalChildByTag("count"); ok {
			response.Content = []waBinary.Node{{Tag: "count", Attrs: waBinary.Attrs{"value": "50"}}}
		} else if collection, ok := n.GetOptionalChildByTag("sync", "collection"); ok {
			// Empty bootstrap collections, not a remote app-state emulator.
			if _, patch := collection.GetOptionalChildByTag("patch"); patch {
				f.patches.Add(1)
			}
			response.Content = []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": collection.Attrs["name"], "has_more_patches": "false"}}}}}
		}
		return p.write(f.ctx, response)
	}
	return nil
}

func (f *keepTransport) decryptOutbound(n *waBinary.Node) error {
	participants := n.GetChildByTag("participants")
	for _, to := range participants.GetChildren() {
		if to.Attrs["jid"] != *f.recipient.ID {
			continue
		}
		enc := to.GetChildByTag("enc")
		data, ok := enc.Content.([]byte)
		if !ok || enc.Attrs["type"] != "pkmsg" {
			return fmt.Errorf("expected actual Signal prekey ciphertext")
		}
		msg, err := protocol.NewPreKeySignalMessageFromBytes(data, waStore.SignalProtobufSerializer.PreKeySignalMessage, waStore.SignalProtobufSerializer.SignalMessage)
		if err != nil {
			return err
		}
		from := f.self.GetLID().SignalAddress()
		builder := session.NewBuilderFromSignal(f.recipient, from, waStore.SignalProtobufSerializer)
		plain, err := session.NewCipher(builder, from).DecryptMessage(f.ctx, msg)
		if err != nil {
			return fmt.Errorf("real outbound Signal decrypt: %w", err)
		}
		if len(plain) == 0 {
			return fmt.Errorf("empty Signal plaintext")
		}
		padding := int(plain[len(plain)-1])
		if padding < 1 || padding > len(plain) {
			return fmt.Errorf("bad message padding")
		}
		var message waE2E.Message
		if err := proto.Unmarshal(plain[:len(plain)-padding], &message); err != nil {
			return err
		}
		if message.GetConversation() != "synthetic automatic reply" && message.GetExtendedTextMessage().GetText() != "synthetic automatic reply" {
			return fmt.Errorf("decrypted outbound text differs from Cobra argument")
		}
		return nil
	}
	return fmt.Errorf("no encrypted recipient in message")
}

func (f *keepTransport) sender(from types.JID) func(string, types.JID, time.Time) waBinary.Node {
	sender := f.device(filepath.Join(f.t.TempDir(), "sender.db"), from)
	r := f.self
	bundle := prekey.NewBundle(r.RegistrationID, uint32(r.ID.Device), optional.NewEmptyUint32(), r.SignedPreKey.KeyID,
		nil, ecc.NewDjbECPublicKey(*r.SignedPreKey.Pub), *r.SignedPreKey.Signature, identity.NewKey(ecc.NewDjbECPublicKey(*r.IdentityKey.Pub)))
	builder := session.NewBuilderFromSignal(sender, r.ID.SignalAddress(), waStore.SignalProtobufSerializer)
	if err := builder.ProcessBundle(f.ctx, bundle); err != nil {
		f.t.Fatal(err)
	}
	signal := session.NewCipher(builder, r.ID.SignalAddress())
	return func(id string, chat types.JID, at time.Time) waBinary.Node {
		f.t.Helper()
		text := "synthetic transport proof"
		if from.User == f.self.GetLID().User {
			text = "synthetic automatic reply"
		}
		plain, err := proto.Marshal(&waE2E.Message{Conversation: proto.String(text)})
		if err != nil {
			f.t.Fatal(err)
		}
		encrypted, err := signal.Encrypt(f.ctx, append(plain, 1))
		if err != nil {
			f.t.Fatal(err)
		}
		kind := "msg"
		if encrypted.Type() == protocol.PREKEY_TYPE {
			kind = "pkmsg"
		}
		return waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{"id": id, "from": from, "recipient": chat, "type": "text", "t": at.Unix()},
			Content: []waBinary.Node{{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": kind}, Content: encrypted.Serialize()}}}
	}
}

// Noise XX responder copied from the existing wa_test transport harness.
func keepTransportCertificate(t *testing.T, root, static *keys.KeyPair) []byte {
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

type keepTransportNoise struct {
	conn                    *websocket.Conn
	done                    chan struct{}
	readCipher, writeCipher cipher.AEAD
	readCount, writeCount   uint32
	writeMu                 sync.Mutex
}

func keepTransportHandshake(ctx context.Context, conn *websocket.Conn, static *keys.KeyPair, cert []byte) (*keepTransportNoise, error) {
	_, hello, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(hello, socket.WAConnHeader) {
		return nil, fmt.Errorf("missing WA header")
	}
	hello, err = keepTransportFrame(hello[len(socket.WAConnHeader):])
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
	if err := keepTransportWriteFrame(ctx, conn, response); err != nil {
		return nil, err
	}
	_, finish, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	finish, err = keepTransportFrame(finish)
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
	peer := &keepTransportNoise{conn: conn}
	if peer.readCipher, err = makeCipher(readKey); err != nil {
		return nil, err
	}
	peer.writeCipher, err = makeCipher(writeKey)
	return peer, err
}

func keepTransportFrame(message []byte) ([]byte, error) {
	if len(message) < 3 {
		return nil, fmt.Errorf("short frame")
	}
	length := int(message[0])<<16 | int(message[1])<<8 | int(message[2])
	if length != len(message)-3 {
		return nil, fmt.Errorf("frame length %d != %d", length, len(message)-3)
	}
	return message[3:], nil
}

func keepTransportWriteFrame(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	frame := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	return conn.Write(ctx, websocket.MessageBinary, append(frame, payload...))
}

func keepTransportIV(count uint32) []byte {
	iv := make([]byte, 12)
	binary.BigEndian.PutUint32(iv[8:], count)
	return iv
}

func (p *keepTransportNoise) read(ctx context.Context) (*waBinary.Node, error) {
	_, frame, err := p.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	frame, err = keepTransportFrame(frame)
	if err != nil {
		return nil, err
	}
	plain, err := p.readCipher.Open(nil, keepTransportIV(p.readCount), frame, nil)
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

func (p *keepTransportNoise) write(ctx context.Context, node waBinary.Node) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	encoded, err := waBinary.Marshal(node)
	if err != nil {
		return err
	}
	ciphertext := p.writeCipher.Seal(nil, keepTransportIV(p.writeCount), encoded, nil)
	p.writeCount++
	return keepTransportWriteFrame(ctx, p.conn, ciphertext)
}
