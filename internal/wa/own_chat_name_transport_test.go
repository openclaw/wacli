package wa_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/util/optional"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"
)

const nameTransportAccountName = "Shop Account"

// Real App.Sync -> whatsmeow -> Noise XX/WebSocket -> in-process peer. A message
// this account sent from another of its devices to a chat never seen before
// crosses the socket Signal-encrypted, carrying the account's own push name in
// the stanza's notify attribute. The test logs the stored chat name and, when
// WACLI_PROOF_CLI names a built CLI, what `chats list` prints, then delivers a
// message from the customer, which must name the chat, and one more own
// message, which must leave that name alone.
//
// WACLI_PROOF_KEEP_STORE=dir keeps the store there, so another build can open
// it afterwards. Not proven: pairing/login, the remote service's policy, the
// phone UI.
func TestOwnMessageNamesNewChatRealTransport(t *testing.T) {
	// The exported SDK trust root is process-wide: never run this in parallel.
	root, static, cert := nameTransportTrust(t)
	dir := nameTransportDir(t)
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	chat := types.NewJID("5551230000", types.DefaultUserServer)
	base := time.Unix(1700000000, 0).UTC()

	nameTransportSync(t, dir, root, static, cert, nameTransportDeliver(t, chat, "first sync, own message to a new chat", []nameTransportMessage{
		{id: "own-1", own: true, notify: nameTransportAccountName, at: base},
	}))
	got := nameTransportChatName(t, dir, chat, "after the own message")
	nameTransportCLI(t, dir, "after the own message")
	nameTransportSnapshot(t, dir)
	if got != chat.String() {
		t.Errorf("chat name after an own message = %q, want the JID %q", got, chat)
	}

	nameTransportSync(t, dir, root, static, cert, nameTransportDeliver(t, chat, "reopened store, message from the customer", []nameTransportMessage{
		{id: "in-1", own: false, notify: "Ana", at: base.Add(time.Minute)},
	}))
	got = nameTransportChatName(t, dir, chat, "after the customer's message")
	if got != "Ana" {
		t.Errorf("chat name after the customer's message = %q, want Ana", got)
	}

	nameTransportSync(t, dir, root, static, cert, nameTransportDeliver(t, chat, "reopened store, another own message", []nameTransportMessage{
		{id: "own-2", own: true, notify: nameTransportAccountName, at: base.Add(2 * time.Minute)},
	}))
	got = nameTransportChatName(t, dir, chat, "after a later own message")
	output := nameTransportCLI(t, dir, "after a later own message")
	if got != "Ana" {
		t.Errorf("chat name after a later own message = %q, want it kept as Ana", got)
	}
	if os.Getenv("WACLI_PROOF_CLI") != "" && strings.Contains(output, nameTransportAccountName) {
		t.Errorf("CLI chats list shows the account's own name %q", nameTransportAccountName)
	}
}

// A store written before this change already names the chat after the account.
// WACLI_PROOF_UPGRADE_STORE names a store the previous build wrote with
// TestOwnMessageNamesNewChatRealTransport; it is opened by this build without
// any migration, the stored name is shown as it is, an own message leaves it,
// and the customer's message names the chat.
func TestOwnMessageUpgradedStoreRealTransport(t *testing.T) {
	dir := os.Getenv("WACLI_PROOF_UPGRADE_STORE")
	if dir == "" {
		t.Skip("WACLI_PROOF_UPGRADE_STORE not set")
	}
	root, static, cert := nameTransportTrust(t)
	chat := types.NewJID("5551230000", types.DefaultUserServer)
	base := time.Unix(1700001000, 0).UTC()

	if got := nameTransportChatName(t, dir, chat, "legacy store, as opened"); got != nameTransportAccountName {
		t.Fatalf("legacy store names the chat %q, want %q written by the previous build", got, nameTransportAccountName)
	}
	nameTransportCLI(t, dir, "legacy store, as opened")

	nameTransportSync(t, dir, root, static, cert, nameTransportDeliver(t, chat, "legacy store, own message", []nameTransportMessage{
		{id: "own-3", own: true, notify: nameTransportAccountName, at: base},
	}))
	if got := nameTransportChatName(t, dir, chat, "legacy store, after an own message"); got != nameTransportAccountName {
		t.Errorf("chat name = %q, want the stored name left alone", got)
	}

	nameTransportSync(t, dir, root, static, cert, nameTransportDeliver(t, chat, "legacy store, message from the customer", []nameTransportMessage{
		{id: "in-3", own: false, notify: "Ana", at: base.Add(time.Minute)},
	}))
	if got := nameTransportChatName(t, dir, chat, "legacy store, after the customer's message"); got != "Ana" {
		t.Errorf("chat name = %q, want Ana", got)
	}
	nameTransportCLI(t, dir, "legacy store, after the customer's message")
}

type nameTransportMessage struct {
	id     string
	own    bool
	notify string
	at     time.Time
}

func nameTransportTrust(t *testing.T) (root, static *keys.KeyPair, cert []byte) {
	t.Helper()
	root = keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
	static = keys.NewKeyPair()
	return root, static, ownTransportCertificate(t, root, static)
}

func nameTransportDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("WACLI_PROOF_KEEP_STORE"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	return t.TempDir()
}

// nameTransportSnapshot copies the store to WACLI_PROOF_SNAPSHOT_DIR, when set,
// so a later build can open the state this build left after the first message.
func nameTransportSnapshot(t *testing.T, dir string) {
	t.Helper()
	dst := os.Getenv("WACLI_PROOF_SNAPSHOT_DIR")
	if dst == "" {
		return
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "LOCK" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("SNAPSHOT: store after the first message copied for an upgrade check")
}

func nameTransportChatName(t *testing.T, dir string, chat types.JID, label string) string {
	t.Helper()
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c, err := a.DB().GetChat(chat.String())
	if err != nil {
		t.Fatalf("%s: chat not stored: %v", label, err)
	}
	t.Logf("STORE %s: chats.name = %q", label, c.Name)
	return c.Name
}

func nameTransportCLI(t *testing.T, dir, label string) string {
	t.Helper()
	cli := os.Getenv("WACLI_PROOF_CLI")
	if cli == "" {
		t.Logf("CLI step skipped for %s (WACLI_PROOF_CLI not set)", label)
		return ""
	}
	cmd := exec.Command(cli, "--store", dir, "--read-only", "chats", "list")
	cmd.Env = append(os.Environ(), "TZ=UTC", "HOME="+dir, "XDG_STATE_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	output, err := cmd.CombinedOutput()
	t.Logf("CLI `wacli chats list` %s (real command, read-only):\n%s", label, output)
	if err != nil {
		t.Fatalf("CLI: %v", err)
	}
	return string(output)
}

func nameTransportDeliver(t *testing.T, chat types.JID, label string, items []nameTransportMessage) func(context.Context, *app.App, *whatsmeow.Client, *ownTransportNoise, <-chan string, <-chan string) error {
	return func(ctx context.Context, a *app.App, cli *whatsmeow.Client, peer *ownTransportNoise, seen, receipts <-chan string) error {
		customer := nameTransportSender(t, t.Context(), cli.Store, chat)
		otherDevice := *cli.Store.ID
		otherDevice.Device = 1
		own := nameTransportSender(t, t.Context(), cli.Store, otherDevice)
		for _, item := range items {
			makeNode := customer
			who := "customer"
			if item.own {
				makeNode, who = own, "this account's other device"
			}
			text := "synthetic message " + item.id
			if err := peer.write(ctx, makeNode(item.id, chat, item.at, item.notify, &waE2E.Message{Conversation: proto.String(text)})); err != nil {
				return err
			}
			for _, ch := range []<-chan string{seen, receipts} {
				select {
				case got := <-ch:
					if got != item.id {
						t.Fatalf("runtime/receipt ID = %s, want %s", got, item.id)
					}
				case <-ctx.Done():
					t.Fatal("runtime did not persist/receipt message")
				}
			}
			t.Logf("WIRE %s: Signal-encrypted message %s from %s, stanza notify=%q; SDK decrypted, App.Sync stored it, receipt acknowledged", label, item.id, who, item.notify)
		}
		return nil
	}
}

// nameTransportSync runs one production App.Sync against a fresh in-process
// peer. The harness pieces are the ones from own_message_transport_test.go.
func nameTransportSync(t *testing.T, dir string, root, static *keys.KeyPair, cert []byte, run func(context.Context, *app.App, *whatsmeow.Client, *ownTransportNoise, <-chan string, <-chan string) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	cli := wa.RawClient(a.WA().(*wa.Client))
	cli.EnableAutoReconnect = false
	peers := make(chan *ownTransportNoise, 1)
	receipts := make(chan string, 32)
	disconnected := make(chan struct{}, 1)
	handler := cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Disconnected); ok {
			disconnected <- struct{}{}
		}
	})
	defer cli.RemoveEventHandler(handler)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		peer, err := ownTransportHandshake(ctx, conn, static, cert)
		if err != nil {
			t.Errorf("Noise handshake: %v", err)
			return
		}
		t.Log("NOISE XX: ConnectContext verified test-CA certificate; synthetic paired device")
		peers <- peer
		for {
			node, err := peer.read(ctx)
			if err != nil {
				return
			}
			if node.Tag == "receipt" {
				if err := peer.write(ctx, waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "class": "receipt", "type": node.Attrs["type"], "from": node.Attrs["to"]}}); err != nil {
					return
				}
				receipts <- fmt.Sprint(node.Attrs["id"])
			}
			if node.Tag == "iq" {
				response := waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "type": "result", "from": types.ServerJID}}
				if collection, ok := node.GetOptionalChildByTag("sync", "collection"); ok {
					response.Content = []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": collection.Attrs["name"], "has_more_patches": "false"}}}}}
				}
				if err := peer.write(ctx, response); err != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	cli.MessengerConfig = &whatsmeow.MessengerConfig{WebsocketURL: "ws" + strings.TrimPrefix(server.URL, "http"), BaseURL: server.URL}
	cli.SetWebsocketHTTPClient(server.Client())
	if err := cli.ConnectContext(ctx); err != nil {
		t.Fatal(err)
	}
	peer := <-peers
	defer func() {
		_ = peer.conn.Close(websocket.StatusNormalClosure, "proof complete")
		select {
		case <-disconnected:
		case <-time.After(5 * time.Second):
			t.Error("SDK did not drain disconnect")
		}
	}()
	_, err = a.Sync(ctx, app.SyncOptions{Mode: app.SyncModeOnce, IdleExit: 20 * time.Millisecond, PresenceMode: app.SyncPresenceModeQuiet, AfterConnect: func(ctx context.Context) error {
		seen := make(chan string, 16)
		observer := cli.AddEventHandler(func(evt any) {
			if msg, ok := evt.(*events.Message); ok {
				seen <- msg.Info.ID
			}
		})
		defer cli.RemoveEventHandler(observer)
		return run(ctx, a, cli, peer, seen, receipts)
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// nameTransportSender is ownTransportSender for an arbitrary message that also
// sets the stanza's notify attribute, the sender's push name.
func nameTransportSender(t *testing.T, ctx context.Context, receiver *waStore.Device, from types.JID) func(string, types.JID, time.Time, string, *waE2E.Message) waBinary.Node {
	t.Helper()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "peer.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	sender := container.NewDevice()
	sender.ID = &from
	sender.Account = receiver.Account
	if err := sender.Save(ctx); err != nil {
		t.Fatal(err)
	}
	bundle := prekey.NewBundle(receiver.RegistrationID, uint32(receiver.ID.Device), optional.NewEmptyUint32(), receiver.SignedPreKey.KeyID,
		nil, ecc.NewDjbECPublicKey(*receiver.SignedPreKey.Pub), *receiver.SignedPreKey.Signature, identity.NewKey(ecc.NewDjbECPublicKey(*receiver.IdentityKey.Pub)))
	builder := session.NewBuilderFromSignal(sender, receiver.ID.SignalAddress(), waStore.SignalProtobufSerializer)
	if err := builder.ProcessBundle(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	cipher := session.NewCipher(builder, receiver.ID.SignalAddress())
	return func(id string, chat types.JID, at time.Time, notify string, message *waE2E.Message) waBinary.Node {
		t.Helper()
		plain, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		// Version 2 uses PKCS#7-like padding; a single 0x01 is valid.
		encrypted, err := cipher.Encrypt(ctx, append(plain, 1))
		if err != nil {
			t.Fatal(err)
		}
		kind := "msg"
		if encrypted.Type() == protocol.PREKEY_TYPE {
			kind = "pkmsg"
		}
		attrs := waBinary.Attrs{"id": id, "from": from, "recipient": chat, "type": "text", "t": at.Unix()}
		if notify != "" {
			attrs["notify"] = notify
		}
		return waBinary.Node{Tag: "message", Attrs: attrs, Content: []waBinary.Node{
			{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": kind}, Content: encrypted.Serialize()},
		}}
	}
}
