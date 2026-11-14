package wa_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// Real App.Sync, Signal decryption, Noise/WebSocket and SQL persistence, followed
// by the production receipt API used by chats mark-read --receipts. This does
// not prove Cobra/IPC wiring, pairing/login, remote service policy or phone UI.
// The SDK trust root is process-wide: do not run this test in parallel.
func TestSemanticUnreadRealTransport(t *testing.T) {
	root := keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
	static := keys.NewKeyPair()
	cert := ownTransportCertificate(t, root, static)
	peers := make(chan *ownTransportNoise, 1)
	delivered := make(chan string, 32)
	read := make(chan []string, 16)
	acked := make(chan string, 16)
	disconnected := make(chan struct{}, 1)
	handler := cli.AddEventHandler(func(evt any) {
		if _, ok := evt.(*events.Disconnected); ok {
			disconnected <- struct{}{}
		}
	})
	t.Cleanup(func() { cli.RemoveEventHandler(handler) })
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
		t.Log("WIRE Noise XX certificate verified; synthetic paired receiver; Signal-encrypted incoming stanzas")
		peers <- peer
		for {
			node, err := peer.read(ctx)
			if err != nil {
				return
			}
			switch node.Tag {
			case "ack":
				if fmt.Sprint(node.Attrs["class"]) == "receipt" {
					t.Logf("WIRE receiver -> peer: acknowledged incoming read-self receipt id=%v", node.Attrs["id"])
					acked <- fmt.Sprint(node.Attrs["id"])
				}
			case "receipt":
				id, kind := fmt.Sprint(node.Attrs["id"]), fmt.Sprint(node.Attrs["type"])
				ids := []string{id}
				if list, ok := node.GetOptionalChildByTag("list"); ok {
					for _, item := range list.GetChildren() {
						ids = append(ids, fmt.Sprint(item.Attrs["id"]))
					}
				}
				t.Logf("WIRE receiver -> peer: receipt type=%s ids=%v; peer acknowledges", kind, ids)
				if err := peer.write(ctx, waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": id, "class": "receipt", "type": node.Attrs["type"], "from": node.Attrs["to"]}}); err != nil {
					return
				}
				switch kind {
				case "inactive", "sender", "<nil>":
					delivered <- id
				case "read":
					read <- ids
				default:
					t.Errorf("unexpected wire receipt type %q", kind)
				}
			case "iq":
				response := waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "type": "result", "from": types.ServerJID}}
				if collection, ok := node.GetOptionalChildByTag("sync", "collection"); ok {
					response.Content = []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": collection.Attrs["name"], "has_more_patches": "false"}}}}}
				}
				if _, ok := node.GetOptionalChildByTag("privacy"); ok {
					t.Log("WIRE privacy query -> readreceipts=all (local peer policy, not a remote-service assertion)")
					response.Content = []waBinary.Node{{Tag: "privacy", Content: []waBinary.Node{{Tag: "category", Attrs: waBinary.Attrs{"name": "readreceipts", "value": "all"}}}}}
				}
				if err := peer.write(ctx, response); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	cli.MessengerConfig = &whatsmeow.MessengerConfig{WebsocketURL: "ws" + strings.TrimPrefix(server.URL, "http"), BaseURL: server.URL}
	cli.SetWebsocketHTTPClient(server.Client())
	if err := cli.ConnectContext(ctx); err != nil {
		t.Fatal(err)
	}
	peer := <-peers
	t.Cleanup(func() {
		_ = peer.conn.Close(websocket.StatusNormalClosure, "proof complete")
		select {
		case <-disconnected:
		case <-time.After(5 * time.Second):
			t.Error("SDK did not drain disconnect")
		}
	})
	_, err = a.Sync(ctx, app.SyncOptions{Mode: app.SyncModeOnce, IdleExit: 20 * time.Millisecond, PresenceMode: app.SyncPresenceModeQuiet, AfterConnect: func(ctx context.Context) error {
		seen := make(chan string, 32)
		seenRead := make(chan string, 16)
		observer := cli.AddEventHandler(func(evt any) {
			if msg, ok := evt.(*events.Message); ok {
				seen <- msg.Info.ID
			}
			if receipt, ok := evt.(*events.Receipt); ok && receipt.Type == types.ReceiptTypeReadSelf {
				seenRead <- receipt.MessageIDs[0]
			}
		})
		defer cli.RemoveEventHandler(observer)
		for i, scenario := range []string{"kinds", "dedup", "bounded", "semantic-receipt", "bounded-sync-receipt"} {
			t.Run(scenario, func(t *testing.T) {
				chat := types.NewJID(fmt.Sprintf("555123%04d", i), types.DefaultUserServer)
				makeNode := semanticTransportSender(t, ctx, cli.Store, chat)
				base := time.Unix(1700000000, 0)
				send := func(id string, offset int, message *waE2E.Message) {
					t.Helper()
					t.Logf("WIRE peer -> App.Sync: id=%s original identity/time, fresh Signal encryption; conversation_present=%v", id, message.Conversation != nil)
					if err := peer.write(ctx, makeNode(id, chat, base.Add(time.Duration(offset)*time.Second), message)); err != nil {
						t.Fatal(err)
					}
					// App.Sync's handler is registered before this observer.
					// Drain both runtime dispatch and the wire delivery receipt.
					for _, ch := range []<-chan string{seen, delivered} {
						select {
						case got := <-ch:
							if got != id {
								t.Fatalf("runtime/delivery ID=%s, want %s", got, id)
							}
						case <-ctx.Done():
							t.Fatal("runtime/delivery timed out")
						}
					}
				}
				state := func(label string, rows int64, count int) {
					t.Helper()
					c, err := a.DB().GetChat(chat.String())
					if err != nil {
						t.Fatal(err)
					}
					messages, err := a.DB().ListMessages(store.ListMessagesParams{ChatJID: chat.String(), Limit: 100})
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("STORE %s: rows=%d unread=%v unread_count=%d; want rows=%d unread_count=%d", label, len(messages), c.Unread, c.UnreadCount, rows, count)
					if int64(len(messages)) != rows || c.Unread != (count > 0) || c.UnreadCount != count {
						t.Errorf("%s: stored rows/unread differ from expected", label)
					}
				}
				receipt := func(id string, want int) bool {
					t.Helper()
					n, kind, err := a.MarkChatReadWithReceiptsThrough(ctx, chat, id)
					// This is the same production dispatcher as direct and
					// delegated chats mark-read --receipts --through-message-id.
					t.Logf("APP bounded receipt through=%s selected=%d result=%s error=%v", id, n, kind, err)
					if err != nil || n != want {
						t.Errorf("semantic receipt selection: got %d, want %d, error=%v", n, want, err)
						return false
					}
					select {
					case ids := <-read:
						if len(ids) != 1 || ids[0] != id {
							t.Errorf("wire read IDs=%v, want only %s", ids, id)
						}
					case <-ctx.Done():
						t.Fatal("read receipt did not cross Noise/WebSocket")
					}
					return true
				}
				readSelf := func(id string) {
					t.Helper()
					t.Logf("WIRE peer -> App.Sync: read-self receipt for %s; read-at time is newer than both messages, stored ID still bounds the read", id)
					if err := peer.write(ctx, waBinary.Node{Tag: "receipt", Attrs: waBinary.Attrs{
						"from": chat, "id": id, "type": "read-self", "t": base.Add(10 * time.Minute).Unix(),
					}}); err != nil {
						t.Fatal(err)
					}
					for _, ch := range []<-chan string{seenRead, acked} {
						select {
						case got := <-ch:
							if got != id {
								t.Fatalf("incoming receipt runtime/ack ID=%s, want %s", got, id)
							}
						case <-ctx.Done():
							t.Fatal("incoming receipt runtime/ack timed out")
						}
					}
				}
				event := &waE2E.Message{EventMessage: &waE2E.EventMessage{Name: proto.String("Synthetic meeting"), StartTime: proto.Int64(base.Add(time.Hour).Unix())}}
				switch scenario {
				case "kinds":
					for j, fixture := range []struct {
						kind string
						msg  *waE2E.Message
					}{
						{"poll", &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{Name: proto.String("Synthetic choice"), Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("One")}}, SelectableOptionsCount: proto.Uint32(1)}}},
						{"contact", &waE2E.Message{ContactMessage: &waE2E.ContactMessage{}}},
						{"contacts", &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{}}},
						{"event", event},
					} {
						send(fixture.kind, j, fixture.msg)
						m, err := a.DB().GetMessage(chat.String(), fixture.kind)
						if err != nil {
							t.Fatal(err)
						}
						t.Logf("STORE incoming id=%s kind=%s text=%q display=%q media_type=%q", fixture.kind, m.Kind, m.Text, m.DisplayText, m.MediaType)
						if m.Kind != fixture.kind || m.MediaType != "" {
							t.Errorf("incoming semantic kind/media mismatch")
						}
						if fixture.kind == "event" && (m.Text != "" || m.DisplayText != "(message)") {
							t.Errorf("event must be stored as semantic-only content")
						}
					}
					state("all incoming semantic messages, no conversation text on wire", 4, 4)
				case "dedup":
					send("replay-event", 0, event)
					state("first semantic-only delivery", 1, 1)
					send("replay-event", 0, event)
					state("duplicate before reading counts once", 1, 1)
					readSelf("replay-event")
					state("read semantic-only message through App.Sync", 1, 0)
					send("replay-event", 0, event)
					state("duplicate after reading cannot resurrect unread", 1, 0)
				case "bounded":
					send("older-text", 0, &waE2E.Message{Conversation: proto.String("Synthetic boundary")})
					send("later-event", 60, event)
					state("text then later semantic-only event", 2, 2)
					if receipt("older-text", 1) {
						state("bounded text read preserves later semantic-only event", 2, 1)
					}
					if receipt("later-event", 1) {
						state("later event remains selectable until explicitly read", 2, 0)
					}
				case "semantic-receipt":
					send("semantic-anchor", 0, event)
					state("semantic-only anchor eligible for explicit receipt", 1, 1)
					if receipt("semantic-anchor", 1) {
						state("semantic-only receipt crossed wire and cleared local unread", 1, 0)
					}
				case "bounded-sync-receipt":
					send("sync-older-text", 0, &waE2E.Message{Conversation: proto.String("Synthetic sync boundary")})
					send("sync-later-event", 60, event)
					state("before incoming bounded read-self receipt", 2, 2)
					readSelf("sync-older-text")
					state("App.Sync bounded recount preserves later semantic-only event", 2, 1)
				}
			})
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// Variant of ownTransportSender accepting arbitrary protobuf payloads. Replays
// keep the stanza ID and timestamp but use a new valid Signal counter; replaying
// identical ciphertext would only test the SDK's cryptographic replay guard.
func semanticTransportSender(t *testing.T, ctx context.Context, receiver *waStore.Device, from types.JID) func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node {
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
