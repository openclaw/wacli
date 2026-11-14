package wa_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
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
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/util/optional"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"
)

// Real App.Sync -> whatsmeow -> Noise XX/WebSocket -> in-process peer. Incoming
// messages, an own-device reply, a read receipt and HistorySyncNotification
// protocol messages (inline bootstrap payload, no media download) all cross the
// socket Signal-encrypted before the production runtime persists them. The test
// logs the unread state and the persisted read position after every step,
// reopens the store (second App.Sync) and replays the history.
// Not proven: pairing/login, the remote service's history policy, the phone UI.
func TestReadPositionRealTransport(t *testing.T) {
	// The exported SDK trust root is process-wide: never run this in parallel.
	root := keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
	static := keys.NewKeyPair()
	cert := ownTransportCertificate(t, root, static)
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	chatA := types.NewJID("proofchat", types.DefaultUserServer)
	chatB := types.NewJID("proofchatb", types.DefaultUserServer)
	base := time.Unix(1700000000, 0)
	var incomingFrom, ownDevice, ownPrimary func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node
	text := func(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }
	historyMsg := func(id string, at time.Time) *waHistorySync.HistorySyncMsg {
		return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key:              &waCommon.MessageKey{ID: proto.String(id), FromMe: proto.Bool(false)},
			MessageTimestamp: proto.Uint64(uint64(at.Unix())),
			Message:          text("history " + id),
		}}
	}

	// step runs one Sync against a fresh peer and exposes the wire helpers.
	step := func(label string, body func(ctx context.Context, w *readTransportWire)) {
		readTransportSync(t, dir, root, static, cert, func(ctx context.Context, a *app.App, cli *whatsmeow.Client, peer *ownTransportNoise, seen, receipts <-chan string, readEvents <-chan struct{}) {
			if incomingFrom == nil {
				own := *cli.Store.ID
				device := own
				device.Device = 1
				primary := own.ToNonAD()
				incomingFrom = readTransportSender(t, t.Context(), cli.Store, chatA)
				ownDevice = readTransportSender(t, t.Context(), cli.Store, device)
				ownPrimary = readTransportSender(t, t.Context(), cli.Store, primary)
			}
			t.Logf("== %s", label)
			body(ctx, &readTransportWire{t: t, a: a, cli: cli, peer: peer, seen: seen, receipts: receipts, reads: readEvents, dir: dir, done: map[string]int{}})
		})
	}
	incoming := func(w *readTransportWire, id string, at time.Time) {
		w.deliver("incoming "+id, id, incomingFrom(id, chatA, at, text("synthetic "+id)))
	}

	step("sync 1: two incoming messages, own-device reply, stale history", func(ctx context.Context, w *readTransportWire) {
		incoming(w, "m1", base)
		incoming(w, "m2", base.Add(time.Second))
		w.state(chatA, "two incoming messages", true, 2)
		w.deliver("own-device reply r1", "r1", ownDevice("r1", chatA, base.Add(2*time.Second), text("synthetic reply")))
		w.state(chatA, "after own-device reply (read boundary persisted)", false, 0)
		w.history("stale history snapshot reporting unread_count=2", "h1", ownPrimary, chatA, 2, nil)
		w.state(chatA, "after stale history (must stay read)", false, 0)
	})
	step("sync 2 (store reopened): history replay, then a newer incoming message", func(ctx context.Context, w *readTransportWire) {
		w.state(chatA, "after reopening the store", false, 0)
		w.history("same stale history replayed after restart", "h2", ownPrimary, chatA, 2, nil)
		w.state(chatA, "after replayed stale history (must stay read)", false, 0)
		incoming(w, "m3", base.Add(10*time.Second))
		w.state(chatA, "after incoming m3 beyond the boundary", true, 1)
		w.history("history reporting unread_count=1 with m3", "h3", ownPrimary, chatA, 1, []*waHistorySync.HistorySyncMsg{historyMsg("m3", base.Add(10*time.Second))})
		w.state(chatA, "after history with newer unread (still counted)", true, 1)

		// Approximate receipt time: names no stored message, so it clears unread
		// but must not become a durable read position.
		w.history("chat B history: one incoming message", "h4", ownPrimary, chatB, 1, []*waHistorySync.HistorySyncMsg{historyMsg("b1", base.Add(20*time.Second))})
		w.state(chatB, "chat B before the receipt", true, 1)
		w.readReceipt("read-self receipt naming no stored message, sent 5 min later", "unstored-id", chatB, base.Add(320*time.Second))
		w.state(chatB, "after receipt for an unstored message (unread cleared, no durable position)", false, 0)
		w.history("chat B history: an unread message older than the receipt time", "h5", ownPrimary, chatB, 1, []*waHistorySync.HistorySyncMsg{historyMsg("b2", base.Add(260*time.Second))})
		w.state(chatB, "after history with an older unread message (must be counted)", true, 1)
	})
}

type readTransportWire struct {
	t        *testing.T
	a        *app.App
	cli      *whatsmeow.Client
	peer     *ownTransportNoise
	seen     <-chan string
	receipts <-chan string
	reads    <-chan struct{}
	dir      string
	done     map[string]int
}

func (w *readTransportWire) await(ch <-chan string, id string) {
	w.t.Helper()
	for {
		select {
		case got := <-ch:
			if got == id {
				w.done[id]++
				return
			}
			// A protocol message is answered with a second receipt for the same ID.
			if w.done[got] == 0 {
				w.t.Fatalf("runtime/receipt ID = %s, want %s", got, id)
			}
		case <-time.After(10 * time.Second):
			w.t.Fatalf("runtime did not process %s", id)
		}
	}
}

func (w *readTransportWire) deliver(what, id string, node waBinary.Node) {
	w.t.Helper()
	if err := w.peer.write(w.t.Context(), node); err != nil {
		w.t.Fatal(err)
	}
	w.await(w.seen, id)
	w.await(w.receipts, id)
	w.t.Logf("WIRE %s: Signal-encrypted message %s decrypted by the SDK and stored by App.Sync; receipt acknowledged", what, id)
}

func (w *readTransportWire) history(what, id string, sender func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node, chat types.JID, unread uint32, msgs []*waHistorySync.HistorySyncMsg) {
	w.t.Helper()
	data, err := proto.Marshal(&waHistorySync.HistorySync{
		SyncType:      waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
		Conversations: []*waHistorySync.Conversation{{ID: proto.String(chat.String()), UnreadCount: proto.Uint32(unread), Messages: msgs}},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	var packed bytes.Buffer
	zw := zlib.NewWriter(&packed)
	_, _ = zw.Write(data)
	_ = zw.Close()
	notification := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(),
		HistorySyncNotification: &waE2E.HistorySyncNotification{
			SyncType:                          waE2E.HistorySyncType_INITIAL_BOOTSTRAP.Enum(),
			InitialHistBootstrapInlinePayload: packed.Bytes(),
		},
	}}
	if err := w.peer.write(w.t.Context(), sender(id, w.cli.Store.ID.ToNonAD(), time.Now(), notification)); err != nil {
		w.t.Fatal(err)
	}
	// The manual history download runs inside the message handler, so the
	// message being observed means the snapshot has been applied.
	w.await(w.seen, id)
	w.await(w.receipts, id)
	w.t.Logf("WIRE %s: HistorySyncNotification %s (own primary device, inline payload, unread_count=%d, %d messages) decrypted, inflated and applied by App.Sync", what, id, unread, len(msgs))
}

func (w *readTransportWire) readReceipt(what, id string, chat types.JID, at time.Time) {
	w.t.Helper()
	own := *w.cli.Store.ID
	own.Device = 1
	node := waBinary.Node{Tag: "receipt", Attrs: waBinary.Attrs{"id": id, "from": own, "recipient": chat, "type": "read-self", "t": at.Unix()}}
	if err := w.peer.write(w.t.Context(), node); err != nil {
		w.t.Fatal(err)
	}
	select {
	case <-w.reads:
	case <-time.After(10 * time.Second):
		w.t.Fatalf("runtime did not process receipt %s", id)
	}
	w.t.Logf("WIRE %s: receipt for message %s (not stored locally) applied by App.Sync", what, id)
}

func (w *readTransportWire) state(chat types.JID, label string, unread bool, count int) {
	w.t.Helper()
	c, err := w.a.DB().GetChat(chat.String())
	if err != nil {
		w.t.Fatalf("%s: %v", label, err)
	}
	position := "read_through unavailable (column missing)"
	raw, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(w.dir, "wacli.db"), "mode=ro"))
	if err == nil {
		defer raw.Close()
		var ts, rowid int64
		if err := raw.QueryRow(`SELECT read_through_ts, read_through_rowid FROM chats WHERE jid = ?`, chat.String()).Scan(&ts, &rowid); err == nil {
			position = fmt.Sprintf("read_through_ts=%d read_through_rowid=%d", ts, rowid)
		}
	}
	w.t.Logf("STORE %s: %s: unread=%v unread_count=%d %s", chat.User, label, c.Unread, c.UnreadCount, position)
	if c.Unread != unread || c.UnreadCount != count {
		w.t.Errorf("%s: %s: unread=(%v,%d), want (%v,%d)", chat.User, label, c.Unread, c.UnreadCount, unread, count)
	}
}

// readTransportSync runs one production App.Sync against a fresh in-process
// peer, with the Noise/Signal pieces from own_message_transport_test.go.
func readTransportSync(t *testing.T, dir string, root, static *keys.KeyPair, cert []byte, run func(context.Context, *app.App, *whatsmeow.Client, *ownTransportNoise, <-chan string, <-chan string, <-chan struct{})) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
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
		reads := make(chan struct{}, 4)
		observer := cli.AddEventHandler(func(evt any) {
			switch v := evt.(type) {
			case *events.Message:
				seen <- v.Info.ID
			case *events.Receipt:
				reads <- struct{}{}
			}
		})
		defer cli.RemoveEventHandler(observer)
		run(ctx, a, cli, peer, seen, receipts, reads)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// readTransportSender is ownTransportSender for an arbitrary message and sender JID.
func readTransportSender(t *testing.T, ctx context.Context, receiver *waStore.Device, from types.JID) func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node {
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
