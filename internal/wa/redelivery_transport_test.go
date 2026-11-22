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
	"github.com/openclaw/wacli/internal/store"
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

// Real App.Sync -> whatsmeow -> Noise XX/WebSocket -> in-process peer. Three
// messages arrive Signal-encrypted, the store is closed and reopened, and the
// first message is delivered again (fresh ciphertext, same ID) with a stanza
// time 40 minutes later, as a retry or replay carries. The test logs what the
// production sync stored and, when WACLI_PROOF_CLI names a built CLI, what
// `messages list` prints, and requires the conversation order and the first
// message's time to be unchanged.
// Not proven: pairing/login, the remote service's policy, the phone UI.
func TestRedeliveryKeepsOrderRealTransport(t *testing.T) {
	// The exported SDK trust root is process-wide: never run this in parallel.
	root := keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
	static := keys.NewKeyPair()
	cert := ownTransportCertificate(t, root, static)
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	chat := types.NewJID("5551230000", types.DefaultUserServer)
	base := time.Unix(1700000000, 0).UTC()
	ids := []string{"m1", "m2", "m3"}
	var sender func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node

	deliver := func(label string, items []struct {
		id string
		at time.Time
	}) func(context.Context, *app.App, *whatsmeow.Client, *ownTransportNoise, <-chan string, <-chan string) error {
		return func(ctx context.Context, a *app.App, cli *whatsmeow.Client, peer *ownTransportNoise, seen, receipts <-chan string) error {
			if sender == nil {
				sender = redeliveryTransportSender(t, t.Context(), cli.Store, chat)
			}
			for _, item := range items {
				text := "synthetic message " + item.id
				if err := peer.write(ctx, sender(item.id, chat, item.at, &waE2E.Message{Conversation: proto.String(text)})); err != nil {
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
				t.Logf("WIRE %s: Signal-encrypted message %s delivered with stanza t=%s; SDK decrypted, App.Sync stored it, receipt acknowledged", label, item.id, item.at.Format("15:04:05"))
			}
			return nil
		}
	}

	storedOrder := func(label string) ([]string, time.Time) {
		t.Helper()
		a, err := app.New(app.Options{StoreDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		for _, id := range ids {
			m, err := a.DB().GetMessage(chat.String(), id)
			if err != nil {
				t.Fatalf("%s: %s not stored: %v", label, id, err)
			}
			t.Logf("STORE %s: %s ts=%s text=%q", label, id, m.Timestamp.UTC().Format("15:04:05"), m.Text)
		}
		msgs, err := a.DB().ListMessages(store.ListMessagesParams{ChatJID: chat.String(), Limit: 10, Asc: true})
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, m := range msgs {
			order = append(order, m.MsgID)
		}
		first, err := a.DB().GetMessage(chat.String(), "m1")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("STORE %s: conversation order (ts ascending) = %v", label, order)
		return order, first.Timestamp.UTC()
	}

	cli := os.Getenv("WACLI_PROOF_CLI")
	listCLI := func(label string) string {
		if cli == "" {
			t.Logf("CLI step skipped for %s (WACLI_PROOF_CLI not set)", label)
			return ""
		}
		cmd := exec.Command(cli, "--store", dir, "--read-only", "messages", "list", "--chat", chat.String(), "--asc")
		cmd.Env = append(os.Environ(), "TZ=UTC", "HOME="+dir, "XDG_STATE_HOME="+dir, "XDG_CONFIG_HOME="+dir)
		output, err := cmd.CombinedOutput()
		t.Logf("CLI `wacli messages list --chat %s --asc` %s (real command, read-only):\n%s", chat, label, output)
		if err != nil {
			t.Fatalf("CLI: %v", err)
		}
		return string(output)
	}

	type item = struct {
		id string
		at time.Time
	}
	redeliveryTransportSync(t, dir, root, static, cert, deliver("first delivery", []item{
		{"m1", base}, {"m2", base.Add(time.Minute)}, {"m3", base.Add(2 * time.Minute)},
	}))
	order, ts := storedOrder("after first sync")
	listCLI("after first sync")
	if got := strings.Join(order, ","); got != "m1,m2,m3" {
		t.Fatalf("order after first sync = %s, want m1,m2,m3", got)
	}

	redeliveryTransportSync(t, dir, root, static, cert, deliver("redelivery after reopening the store", []item{
		{"m1", base.Add(40 * time.Minute)},
	}))
	order2, ts2 := storedOrder("after redelivery of m1 (stanza t=+40m)")
	output := listCLI("after redelivery of m1")
	if !ts2.Equal(ts) {
		t.Errorf("m1 ts after redelivery = %s, want the original %s", ts2.Format("15:04:05"), ts.Format("15:04:05"))
	}
	if got := strings.Join(order2, ","); got != "m1,m2,m3" {
		t.Errorf("order after redelivery = %s, want m1,m2,m3", got)
	}
	if cli != "" {
		i1, i2, i3 := strings.Index(output, "synthetic message m1"), strings.Index(output, "synthetic message m2"), strings.Index(output, "synthetic message m3")
		if i1 < 0 || i2 < 0 || i3 < 0 || !(i1 < i2 && i2 < i3) {
			t.Errorf("CLI output is not in conversation order m1, m2, m3")
		}
	}
}

// redeliveryTransportSync runs one production App.Sync against a fresh
// in-process peer. The harness pieces are the ones from
// own_message_transport_test.go.
func redeliveryTransportSync(t *testing.T, dir string, root, static *keys.KeyPair, cert []byte, run func(context.Context, *app.App, *whatsmeow.Client, *ownTransportNoise, <-chan string, <-chan string) error) {
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

// redeliveryTransportSender is ownTransportSender for an arbitrary message.
func redeliveryTransportSender(t *testing.T, ctx context.Context, receiver *waStore.Device, from types.JID) func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node {
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
	return func(id string, chat types.JID, at time.Time, message *waE2E.Message) waBinary.Node {
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
		return waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{"id": id, "from": from, "recipient": chat, "type": "text", "t": at.Unix()}, Content: []waBinary.Node{
			{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": kind}, Content: encrypted.Serialize()},
		}}
	}
}
