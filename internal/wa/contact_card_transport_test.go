package wa_test

import (
	"context"
	"database/sql"
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

const (
	contactTransportWaidOnly = "BEGIN:VCARD\nVERSION:3.0\nN:Prueba;Ana;;;\nFN:Ana Prueba\nTEL;type=CELL;waid=550101:\nEND:VCARD"
	contactTransportExtra    = "BEGIN:VCARD\nVERSION:3.0\nFN:Beto Ejemplo\nitem1.TEL;type=CELL;type=VOICE;type=pref;waid=550102:+55 0102\nEND:VCARD"
	contactTransportSecond   = "BEGIN:VCARD\nVERSION:3.0\nFN:Carla Demo\nTEL;type=WORK;waid=550103:\nEND:VCARD"
)

// Real App.Sync -> whatsmeow -> Noise XX/WebSocket -> in-process peer, with
// Signal-encrypted ContactMessage and ContactsArrayMessage stanzas whose vCards
// carry a waid-only TEL (empty value) and extra TEL parameters. It logs what
// the production sync stored (display text, message_contacts rows, and, when
// WACLI_PROOF_CLI names a built CLI, what `messages list` prints), then reopens
// the store and redelivers the same message IDs (fresh ciphertext).
// Not proven: pairing/login, the remote service's policy, the phone UI.
func TestContactCardsRealTransport(t *testing.T) {
	// The exported SDK trust root is process-wide: never run this in parallel.
	root := keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
	static := keys.NewKeyPair()
	cert := ownTransportCertificate(t, root, static)
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	chat := types.NewJID("proofchat", types.DefaultUserServer)
	base := time.Unix(1700000000, 0)
	cardMessage := &waE2E.Message{ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Ana Prueba"), Vcard: proto.String(contactTransportWaidOnly)}}
	arrayMessage := &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{DisplayName: proto.String("2 contacts"), Contacts: []*waE2E.ContactMessage{
		{DisplayName: proto.String("Beto Ejemplo"), Vcard: proto.String(contactTransportExtra)},
		{DisplayName: proto.String("Carla Demo"), Vcard: proto.String(contactTransportSecond)},
	}}}
	var sender func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node

	deliver := func(label string) func(context.Context, *app.App, *whatsmeow.Client, *ownTransportNoise, <-chan string, <-chan string) error {
		return func(ctx context.Context, a *app.App, cli *whatsmeow.Client, peer *ownTransportNoise, seen, receipts <-chan string) error {
			if sender == nil {
				sender = contactTransportSender(t, t.Context(), cli.Store, chat)
			}
			for i, item := range []struct {
				id  string
				msg *waE2E.Message
			}{{"contact-1", cardMessage}, {"contact-2", arrayMessage}} {
				if err := peer.write(ctx, sender(item.id, chat, base.Add(time.Duration(i)*time.Second), item.msg)); err != nil {
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
				t.Logf("WIRE %s: Signal-encrypted %T delivered as %s; SDK decrypted, App.Sync stored it, delivery receipt acknowledged", label, item.msg.GetContactMessage(), item.id)
			}
			return nil
		}
	}

	check := func(label string, wantRows map[string][]string) {
		t.Helper()
		a, err := app.New(app.Options{StoreDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		for id, want := range map[string]string{
			"contact-1": "Contact: Ana Prueba (+550101)",
			"contact-2": "Contacts:\nContact: Beto Ejemplo (+55 0102)\nContact: Carla Demo (+550103)",
		} {
			m, err := a.DB().GetMessage(chat.String(), id)
			if err != nil {
				t.Fatalf("%s: %s not stored: %v", label, id, err)
			}
			t.Logf("STORE %s: %s display_text=%q", label, id, m.DisplayText)
			if m.DisplayText != want {
				t.Errorf("%s: %s display_text = %q, want %q", label, id, m.DisplayText, want)
			}
		}
		raw, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "wacli.db"), "mode=ro"))
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		for _, id := range []string{"contact-1", "contact-2"} {
			rows, err := raw.Query(`SELECT position, display_name, vcard FROM message_contacts WHERE chat_jid = ? AND msg_id = ? ORDER BY position`, chat.String(), id)
			if err != nil {
				t.Errorf("%s: message_contacts query for %s: %v", label, id, err)
				continue
			}
			var got []string
			for rows.Next() {
				var pos int
				var name, vcard string
				if err := rows.Scan(&pos, &name, &vcard); err != nil {
					t.Fatal(err)
				}
				t.Logf("STORE %s: message_contacts %s position=%d display_name=%q vcard=%q", label, id, pos, name, vcard)
				got = append(got, vcard)
			}
			_ = rows.Close()
			if strings.Join(got, "\x00") != strings.Join(wantRows[id], "\x00") {
				t.Errorf("%s: message_contacts rows for %s = %q, want %q", label, id, got, wantRows[id])
			}
		}
		var messages int
		if err := raw.QueryRow(`SELECT count(*) FROM messages WHERE chat_jid = ?`, chat.String()).Scan(&messages); err != nil || messages != 2 {
			t.Errorf("%s: stored messages = %d, %v; want 2", label, messages, err)
		}
	}
	wantRows := map[string][]string{"contact-1": {contactTransportWaidOnly}, "contact-2": {contactTransportExtra, contactTransportSecond}}

	contactTransportSync(t, dir, root, static, cert, deliver("first delivery"))
	check("after first sync", wantRows)

	if cli := os.Getenv("WACLI_PROOF_CLI"); cli != "" {
		cmd := exec.Command(cli, "--store", dir, "--read-only", "messages", "list", "--chat", chat.String(), "--asc")
		cmd.Env = append(os.Environ(), "HOME="+dir, "XDG_STATE_HOME="+dir, "XDG_CONFIG_HOME="+dir)
		output, err := cmd.CombinedOutput()
		t.Logf("CLI `wacli messages list --chat %s --asc` (real command, read-only):\n%s", chat, output)
		if err != nil {
			t.Fatalf("CLI: %v", err)
		}
		if !strings.Contains(string(output), "(+550101)") {
			t.Errorf("CLI output lacks the waid-only number")
		}
	} else {
		t.Log("CLI step skipped (WACLI_PROOF_CLI not set)")
	}

	contactTransportSync(t, dir, root, static, cert, deliver("redelivery after reopening the store"))
	check("after reopen and redelivery", wantRows)
}

// contactTransportSync runs one production App.Sync against a fresh in-process
// peer. The harness pieces are the ones from own_message_transport_test.go.
func contactTransportSync(t *testing.T, dir string, root, static *keys.KeyPair, cert []byte, run func(context.Context, *app.App, *whatsmeow.Client, *ownTransportNoise, <-chan string, <-chan string) error) {
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

// contactTransportSender is ownTransportSender for an arbitrary message.
func contactTransportSender(t *testing.T, ctx context.Context, receiver *waStore.Device, from types.JID) func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node {
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
