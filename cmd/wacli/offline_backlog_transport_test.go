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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/groups"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/util/optional"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCert"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/socket"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"google.golang.org/protobuf/proto"
)

// Issue 580. No `sync` process runs, so the real Cobra command `send text`
// takes the store lock and opens its own connection: the real whatsmeow client
// dials wss://web.whatsapp.com/ws/chat, which the process-wide default
// transport sends to an in-process stand-in over verified TLS and Noise XX.
//
// What the stand-in assumes about WhatsApp's server (not proven here):
//   - it holds what arrived for this device while it was offline and delivers
//     it after the device answers <success> with the `active` passive IQ,
//     between <ib><offline_preview/></ib> and <ib><offline/></ib>;
//   - it drops a message from that queue when the device sends its delivery
//     receipt for it, and a receipt stanza when the device acks it; anything
//     else is delivered again, with the same ciphertext, on the next
//     connection.
//
// The backlog is Signal-encrypted for the device's real keys in session.db.
// In these two tests the stand-in answers the outgoing message only after the
// device has acknowledged everything delivered so far, which WhatsApp does not
// do: it makes the per-kind checks independent of timing. The exit test below
// answers at once. Not proven: pairing/login, WhatsApp's own queue policy, the
// phone.
func TestOneShotSendStoresOfflineBacklogRealTransport(t *testing.T) {
	env := newBacklogEnv(t)
	contact := env.contact
	t0 := time.Unix(1700000000, 0).UTC()
	env.seedOwnMessage("sent-earlier", t0.Add(-10*time.Second))
	env.peer.hold(
		env.incoming("in-1", t0, backlogText("hello from the contact")),
		env.fromOwnDevice("own-1", t0.Add(1*time.Second), backlogText("reply typed on the phone")),
		env.incoming("in-2", t0.Add(2*time.Second), backlogText("second message from the contact")),
		env.incoming("in-edit", t0.Add(3*time.Second), backlogEdit(contact, "in-1", "hello from the contact (edited)")),
		env.incoming("in-react", t0.Add(4*time.Second), backlogReaction(contact, "own-1", "👍")),
		env.incoming("in-revoke", t0.Add(5*time.Second), backlogRevoke(contact, "in-2")),
		env.readReceipt("sent-earlier", t0.Add(6*time.Second)),
		env.incoming("in-4", t0.Add(6*time.Second), backlogText("third message from the contact")),
	)

	stdout, _, err := env.cli("one-shot send while no sync runs", "--json", "send", "text", "--to", contact.User, "--message", "sent by the one-shot")
	if err != nil {
		t.Fatalf("send text: %v", err)
	}
	sentID := backlogSentID(t, stdout)
	env.requireAcknowledgedStored("after `send text` exited")
	if left := env.peer.held(); len(left) != 0 {
		t.Fatalf("the stand-in still holds %v: the one-shot connection did not acknowledge its whole backlog", left)
	}
	rows := env.messages()
	for _, want := range []backlogRow{
		{id: "in-1", text: "hello from the contact (edited)", edited: true},
		{id: "own-1", fromMe: true, text: "reply typed on the phone"},
		{id: "in-2", revoked: true},
		{id: "in-react", reactionTo: "own-1", reaction: "👍"},
	} {
		got, ok := rows[want.id]
		if want.revoked {
			// What a revoke leaves in `text` is the store's business, not this test's.
			got.text = ""
		}
		if !ok || got.fromMe != want.fromMe || got.text != want.text || got.edited != want.edited || got.revoked != want.revoked || got.reactionTo != want.reactionTo || got.reaction != want.reaction {
			t.Fatalf("stored %s = %+v (present=%v), want %+v", want.id, got, ok, want)
		}
	}
	if status := env.receiptStatus("sent-earlier"); status != "read" {
		t.Fatalf("receipt for sent-earlier = %q, want read", status)
	}
	t.Log("CHECK kinds: incoming text, own message from another device, edit, reaction, revoke and a read receipt were each stored by the one-shot connection that acknowledged them")

	// A later sync: one new message, and a replay of in-4, which the one-shot
	// stored, with fresh ciphertext and a later stanza time, as a retry carries.
	env.peer.hold(
		env.incoming("in-3", t0.Add(7*time.Second), backlogText("arrived after the one-shot")),
		env.incoming("in-4", t0.Add(40*time.Minute), backlogText("third message from the contact")),
	)
	if _, _, err := env.cli("later sync", "sync", "--once", "--idle-exit", "1s", "--presence-mode", "quiet"); err != nil {
		t.Fatalf("sync --once: %v", err)
	}
	env.requireAcknowledgedStored("after `sync --once` exited")
	want := []string{"sent-earlier", "in-1", "own-1", "in-2", "in-react", "in-4", "in-3", sentID}
	if got := env.order(); !slices.Equal(got, want) {
		t.Fatalf("messages after the later sync = %v, want %v (each once, in time order)", got, want)
	}
	if got := env.messages()["in-4"]; got.ts != t0.Add(6*time.Second).Unix() {
		t.Fatalf("in-4 after the replay = %+v, want its original time %d", got, t0.Add(6*time.Second).Unix())
	}
	t.Log("CHECK later sync: nothing stored twice or reordered; the replayed in-4 kept its time; in-3 arrived through the sync")
}

// A one-shot command that fails after connecting: the stand-in rejects the
// outgoing message (an error ack), so `send text` exits with an error. What
// the connection acknowledged before is in the store; one more message is
// delivered right after the rejection, and is either stored by the one-shot or
// left queued for the next sync.
func TestOneShotFailedSendKeepsAcknowledgedBacklogRealTransport(t *testing.T) {
	env := newBacklogEnv(t)
	t0 := time.Unix(1700000000, 0).UTC()
	env.peer.failSends(true)
	env.peer.hold(
		env.incoming("in-1", t0, backlogText("hello from the contact")),
		env.fromOwnDevice("own-1", t0.Add(time.Second), backlogText("reply typed on the phone")),
	)
	env.peer.holdAfterSend(env.incoming("in-late", t0.Add(2*time.Second), backlogText("arrived as the send failed")))

	if _, stderr, err := env.cli("one-shot send that the server rejects", "send", "text", "--to", env.contact.User, "--message", "rejected", "--post-send-wait", "0"); err == nil {
		t.Fatalf("send text succeeded; the stand-in rejected it (stderr: %s)", stderr)
	}
	env.requireAcknowledgedStored("after the failed `send text` exited")
	late := "left queued for the next sync"
	if slices.Contains(env.peer.acknowledged(), "in-late") {
		late = "stored and acknowledged by the one-shot"
	}
	t.Logf("CHECK failed command: in-1 and own-1 acknowledged and stored; in-late %s", late)

	if _, _, err := env.cli("later sync", "sync", "--once", "--idle-exit", "1s", "--presence-mode", "quiet"); err != nil {
		t.Fatalf("sync --once: %v", err)
	}
	env.requireAcknowledgedStored("after `sync --once` exited")
	if got, want := env.order(), []string{"in-1", "own-1", "in-late"}; !slices.Equal(got, want) {
		t.Fatalf("messages after the later sync = %v, want %v", got, want)
	}
	if left := env.peer.held(); len(left) != 0 {
		t.Fatalf("the stand-in still holds %v after the sync", left)
	}
}

type backlogEnv struct {
	t       *testing.T
	ctx     context.Context
	dir     string
	device  *waStore.Device
	contact types.JID
	peer    *backlogPeer
	senders map[types.JID]func(id string, chat types.JID, at time.Time, msg *waE2E.Message) waBinary.Node
}

func newBacklogEnv(t *testing.T) *backlogEnv {
	t.Helper()
	root := keys.NewKeyPair()
	prevRoot := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = prevRoot })
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	env := &backlogEnv{
		t:       t,
		ctx:     ctx,
		dir:     dir,
		contact: types.NewJID("5551230000", types.DefaultUserServer),
		senders: map[types.JID]func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node{},
	}
	env.device = backlogPairedDevice(t, ctx, filepath.Join(dir, "session.db"), env.contact)
	static := keys.NewKeyPair()
	env.peer = newBacklogPeer(t, ctx, static, backlogCertificate(t, root, static))
	server := httptest.NewTLSServer(http.HandlerFunc(env.peer.serve))
	t.Cleanup(server.Close)
	backlogRoute(t, server, "web.whatsapp.com:443")
	return env
}

// backlogPairedDevice saves a synthetic linked device in session.db, the way a
// line that was paired earlier has one, and keeps its keys for the senders.
// The contact's LID is already known, as it is for a chat the line has used:
// whatsmeow sends a 1:1 message to the recipient's LID.
func backlogPairedDevice(t *testing.T, ctx context.Context, path string, contact types.JID) *waStore.Device {
	t.Helper()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	device := container.NewDevice()
	device.ID = &types.JID{User: "5550000001", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := device.LIDs.PutLIDMapping(ctx, backlogContactLID, contact); err != nil {
		t.Fatal(err)
	}
	return device
}

func (e *backlogEnv) seedOwnMessage(id string, at time.Time) {
	e.t.Helper()
	db, err := store.Open(filepath.Join(e.dir, "wacli.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertChat(e.contact.String(), "dm", "", at); err != nil {
		e.t.Fatal(err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: e.contact.String(), MsgID: id, SenderName: "me", Timestamp: at, FromMe: true, Text: "sent before the device went offline", DisplayText: "sent before the device went offline"}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *backlogEnv) sender(from types.JID) func(string, types.JID, time.Time, *waE2E.Message) waBinary.Node {
	e.t.Helper()
	if s, ok := e.senders[from]; ok {
		return s
	}
	container, err := sqlstore.New(e.ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(e.t.TempDir(), "peer.db"), "_foreign_keys=on"), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = container.Close() })
	sender := container.NewDevice()
	sender.ID = &from
	sender.Account = e.device.Account
	if err := sender.Save(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	receiver := e.device
	bundle := prekey.NewBundle(receiver.RegistrationID, uint32(receiver.ID.Device), optional.NewEmptyUint32(), receiver.SignedPreKey.KeyID,
		nil, ecc.NewDjbECPublicKey(*receiver.SignedPreKey.Pub), *receiver.SignedPreKey.Signature, identity.NewKey(ecc.NewDjbECPublicKey(*receiver.IdentityKey.Pub)))
	builder := session.NewBuilderFromSignal(sender, receiver.ID.SignalAddress(), waStore.SignalProtobufSerializer)
	if err := builder.ProcessBundle(e.ctx, bundle); err != nil {
		e.t.Fatal(err)
	}
	cipher := session.NewCipher(builder, receiver.ID.SignalAddress())
	s := func(id string, chat types.JID, at time.Time, msg *waE2E.Message) waBinary.Node {
		e.t.Helper()
		plain, err := proto.Marshal(msg)
		if err != nil {
			e.t.Fatal(err)
		}
		// Version 2 uses PKCS#7-like padding; a single 0x01 is valid.
		encrypted, err := cipher.Encrypt(e.ctx, append(plain, 1))
		if err != nil {
			e.t.Fatal(err)
		}
		encType := "msg"
		if encrypted.Type() == protocol.PREKEY_TYPE {
			encType = "pkmsg"
		}
		stanzaType := "text"
		if msg.GetReactionMessage() != nil {
			stanzaType = "reaction"
		}
		return waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{"id": id, "from": from, "recipient": chat, "type": stanzaType, "t": at.Unix()}, Content: []waBinary.Node{
			{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": encType}, Content: encrypted.Serialize()},
		}}
	}
	e.senders[from] = s
	return s
}

func (e *backlogEnv) incoming(id string, at time.Time, msg *waE2E.Message) *backlogItem {
	return &backlogItem{id: id, class: "message", node: e.sender(e.contact)(id, e.contact, at, msg)}
}

func (e *backlogEnv) fromOwnDevice(id string, at time.Time, msg *waE2E.Message) *backlogItem {
	other := *e.device.ID
	other.Device = 1
	return &backlogItem{id: id, class: "message", node: e.sender(other)(id, e.contact, at, msg)}
}

func (e *backlogEnv) readReceipt(msgID string, at time.Time) *backlogItem {
	return &backlogItem{id: msgID, class: "receipt", node: waBinary.Node{Tag: "receipt", Attrs: waBinary.Attrs{"id": msgID, "from": e.contact, "type": "read", "t": at.Unix()}}}
}

func backlogText(text string) *waE2E.Message {
	return &waE2E.Message{Conversation: proto.String(text)}
}

func backlogEdit(chat types.JID, target, text string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key:           &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(false), ID: proto.String(target)},
		EditedMessage: &waE2E.Message{Conversation: proto.String(text)},
	}}
}

func backlogRevoke(chat types.JID, target string) *waE2E.Message {
	return &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(),
		Key:  &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(false), ID: proto.String(target)},
	}}
}

func backlogReaction(chat types.JID, target, emoji string) *waE2E.Message {
	return &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
		Key:               &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(true), ID: proto.String(target)},
		Text:              proto.String(emoji),
		SenderTimestampMS: proto.Int64(1700000004000),
	}}
}

func (e *backlogEnv) cli(label string, args ...string) (string, string, error) {
	e.t.Helper()
	e.t.Logf("$ wacli %s   # %s", strings.Join(args, " "), label)
	stdout, stderr, err := backlogRunCLI(e.t, append([]string{"--store", e.dir}, args...))
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line != "" {
			e.t.Logf("  stdout| %s", line)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line != "" {
			e.t.Logf("  stderr| %s", line)
		}
	}
	e.t.Logf("  exit: err=%v; stand-in: connections=%d acknowledged=%v still held=%v", err, e.peer.connections(), e.peer.acknowledged(), e.peer.held())
	return stdout, stderr, err
}

func backlogSentID(t *testing.T, stdout string) string {
	t.Helper()
	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil || env.Data.ID == "" {
		t.Fatalf("send output %q: %v", stdout, err)
	}
	return env.Data.ID
}

type backlogRow struct {
	id, text, reactionTo, reaction string
	fromMe, edited, revoked        bool
	ts                             int64
}

func (e *backlogEnv) openStore() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(e.dir, "wacli.db"), "mode=ro"))
	if err != nil {
		e.t.Fatal(err)
	}
	return db
}

func (e *backlogEnv) messages() map[string]backlogRow {
	e.t.Helper()
	db := e.openStore()
	defer db.Close()
	rows, err := db.Query(`SELECT msg_id, from_me, COALESCE(text,''), edited, revoked, COALESCE(reaction_to_id,''), COALESCE(reaction_emoji,''), ts FROM messages`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]backlogRow{}
	for rows.Next() {
		var r backlogRow
		if err := rows.Scan(&r.id, &r.fromMe, &r.text, &r.edited, &r.revoked, &r.reactionTo, &r.reaction, &r.ts); err != nil {
			e.t.Fatal(err)
		}
		out[r.id] = r
	}
	return out
}

// order lists every stored message, in every chat, in the order a chat view
// shows them.
func (e *backlogEnv) order() []string {
	e.t.Helper()
	db := e.openStore()
	defer db.Close()
	rows, err := db.Query(`SELECT chat_jid, msg_id FROM messages ORDER BY ts, rowid`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var chat, id string
		if err := rows.Scan(&chat, &id); err != nil {
			e.t.Fatal(err)
		}
		if chat != e.contact.String() {
			e.t.Fatalf("message %s stored under chat %s, want %s", id, chat, e.contact)
		}
		ids = append(ids, id)
	}
	return ids
}

func (e *backlogEnv) receiptStatus(msgID string) string {
	e.t.Helper()
	db := e.openStore()
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT status FROM message_receipts WHERE chat_jid = ? AND msg_id = ?`, e.contact.String(), msgID).Scan(&status); err != nil {
		return ""
	}
	return status
}

// requireAcknowledgedStored is the property of issue 580: whatever this
// device acknowledged to the server is in wacli.db once the command exits.
func (e *backlogEnv) requireAcknowledgedStored(when string) {
	e.t.Helper()
	acked := e.peer.acknowledgedItems()
	if len(acked) == 0 {
		e.t.Fatalf("%s: the stand-in recorded no acknowledgement at all", when)
	}
	rows := e.messages()
	var missing []string
	for _, item := range acked {
		switch item.class {
		case "message":
			target := item.id
			if r, ok := rows[target]; !ok || r.ts == 0 {
				// Edits and revokes change their target instead of adding a row.
				if !e.appliedMutation(item.id, rows) {
					missing = append(missing, item.id)
				}
			}
		case "receipt":
			if e.receiptStatus(item.id) == "" {
				missing = append(missing, "receipt:"+item.id)
			}
		}
	}
	if len(missing) > 0 {
		e.t.Fatalf("%s: acknowledged to the server but not in wacli.db: %v (acknowledged: %v)", when, missing, e.peer.acknowledged())
	}
	e.t.Logf("CHECK %s: every item acknowledged to the stand-in (%v) is in wacli.db", when, e.peer.acknowledged())
}

func (e *backlogEnv) appliedMutation(id string, rows map[string]backlogRow) bool {
	switch id {
	case "in-edit":
		return rows["in-1"].edited
	case "in-revoke":
		return rows["in-2"].revoked
	}
	return false
}

// backlogRoute makes the process-wide default transport (which whatsmeow
// clones when a client is built) dial only host, at the local TLS server,
// trusting only its certificate. Restored on cleanup.
func backlogRoute(t *testing.T, server *httptest.Server, host string) {
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

// backlogRunCLI runs the real root command in-process with stdout/stderr captured.
func backlogRunCLI(t *testing.T, args []string) (string, string, error) {
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

// backlogContactLID is the contact's LID, known to the device (see
// backlogPairedDevice).
var backlogContactLID = types.NewJID("88880000001", types.HiddenUserServer)

type backlogItem struct {
	id    string
	class string // "message": acknowledged by a receipt; "receipt": by an ack
	node  waBinary.Node
}

// backlogPeer stands in for WhatsApp's chat server: it answers login and the
// IQs a send needs, holds a queue of stanzas for the device and forgets each
// one only when the device acknowledges it.
type backlogPeer struct {
	t      *testing.T
	ctx    context.Context
	static *keys.KeyPair
	cert   []byte

	mu        sync.Mutex
	conns     int
	queue     []*backlogItem
	afterSend []*backlogItem
	acked     []*backlogItem
	rejecting bool
	changed   chan struct{}
	// answerAtOnce answers the outgoing message as soon as it arrives, as
	// WhatsApp does, instead of after the device acknowledged the backlog.
	answerAtOnce bool
}

func newBacklogPeer(t *testing.T, ctx context.Context, static *keys.KeyPair, cert []byte) *backlogPeer {
	return &backlogPeer{t: t, ctx: ctx, static: static, cert: cert, changed: make(chan struct{}, 1)}
}

func (p *backlogPeer) hold(items ...*backlogItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = append(p.queue, items...)
}

func (p *backlogPeer) holdAfterSend(items ...*backlogItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.afterSend = append(p.afterSend, items...)
}

func (p *backlogPeer) failSends(v bool) { p.mu.Lock(); defer p.mu.Unlock(); p.rejecting = v }
func (p *backlogPeer) connections() int { p.mu.Lock(); defer p.mu.Unlock(); return p.conns }

func (p *backlogPeer) held() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for _, item := range p.queue {
		ids = append(ids, item.id)
	}
	return ids
}

func (p *backlogPeer) acknowledgedItems() []*backlogItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*backlogItem(nil), p.acked...)
}

func (p *backlogPeer) acknowledged() []string {
	var ids []string
	for _, item := range p.acknowledgedItems() {
		ids = append(ids, item.id)
	}
	return ids
}

// acknowledge forgets the first held item of class with id.
func (p *backlogPeer) acknowledge(class, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, item := range p.queue {
		if item.class == class && item.id == id {
			p.queue = slices.Delete(p.queue, i, i+1)
			p.acked = append(p.acked, item)
			select {
			case p.changed <- struct{}{}:
			default:
			}
			return
		}
	}
}

// waitDrained waits until delivered items are all acknowledged, bounded.
func (p *backlogPeer) waitDrained(delivered []*backlogItem) {
	deadline := time.After(10 * time.Second)
	for {
		p.mu.Lock()
		pending := 0
		for _, item := range delivered {
			if slices.Contains(p.queue, item) {
				pending++
			}
		}
		p.mu.Unlock()
		if pending == 0 {
			return
		}
		select {
		case <-p.changed:
		case <-deadline:
			p.t.Logf("PEER %d delivered item(s) still unacknowledged when the send was answered", pending)
			return
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *backlogPeer) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		p.t.Error(err)
		return
	}
	defer conn.CloseNow()
	noise, err := backlogHandshake(p.ctx, conn, p.static, p.cert)
	if err != nil {
		p.t.Errorf("Noise handshake: %v", err)
		return
	}
	p.mu.Lock()
	p.conns++
	connID := p.conns
	p.mu.Unlock()
	p.t.Logf("PEER conn#%d: Noise XX verified (test CA); synthetic paired device logged in", connID)
	if err := noise.write(p.ctx, waBinary.Node{Tag: "success", Attrs: waBinary.Attrs{"t": time.Now().Unix(), "lid": types.JID{User: "99990000001", Device: 7, Server: types.HiddenUserServer}}}); err != nil {
		return
	}
	var delivered []*backlogItem
	for {
		node, err := noise.read(p.ctx)
		if err != nil {
			p.t.Logf("PEER conn#%d: closed (%v)", connID, err)
			return
		}
		switch node.Tag {
		case "iq":
			reply := waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "type": "result", "from": types.ServerJID}}
			if _, ok := node.GetOptionalChildByTag("count"); ok {
				reply.Content = []waBinary.Node{{Tag: "count", Attrs: waBinary.Attrs{"value": "100"}}}
			} else if _, ok := node.GetOptionalChildByTag("usync"); ok {
				reply.Content = []waBinary.Node{{Tag: "usync", Content: []waBinary.Node{{Tag: "list"}}}}
			} else if collection, ok := node.GetOptionalChildByTag("sync", "collection"); ok {
				reply.Content = []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": collection.Attrs["name"], "has_more_patches": "false"}}}}}
			}
			if err := noise.write(p.ctx, reply); err != nil {
				return
			}
			if _, active := node.GetOptionalChildByTag("active"); active && node.Attrs["xmlns"] == "passive" {
				delivered = p.deliver(connID, noise)
			}
		case "receipt":
			kind := fmt.Sprint(node.Attrs["type"])
			p.t.Logf("PEER conn#%d <- receipt id=%v type=%s", connID, node.Attrs["id"], kind)
			if kind == "retry" {
				p.t.Errorf("device asked for a retry of %v: the synthetic ciphertext did not decrypt", node.Attrs["id"])
				continue
			}
			p.acknowledge("message", fmt.Sprint(node.Attrs["id"]))
			if err := noise.write(p.ctx, waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "class": "receipt", "type": node.Attrs["type"], "from": node.Attrs["to"]}}); err != nil {
				return
			}
		case "ack":
			if node.Attrs["class"] == "receipt" {
				p.t.Logf("PEER conn#%d <- ack class=receipt id=%v", connID, node.Attrs["id"])
				p.acknowledge("receipt", fmt.Sprint(node.Attrs["id"]))
			}
		case "message":
			id := node.Attrs["id"]
			p.t.Logf("PEER conn#%d <- outgoing message id=%v to=%v", connID, id, node.Attrs["to"])
			pending := delivered
			p.mu.Lock()
			atOnce := p.answerAtOnce
			p.mu.Unlock()
			go func() {
				if !atOnce {
					p.waitDrained(pending)
				}
				p.mu.Lock()
				reject := p.rejecting
				late := p.afterSend
				p.afterSend = nil
				p.mu.Unlock()
				ack := waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": id, "class": "message", "t": time.Now().Unix()}}
				if reject {
					ack.Attrs["error"] = "479"
					p.t.Logf("PEER conn#%d -> ack id=%v error=479 (the stand-in rejects this send)", connID, id)
				}
				if err := noise.write(p.ctx, ack); err != nil {
					return
				}
				// Held first: a connection that closes half-way leaves the
				// rest queued, as a server would.
				p.hold(late...)
				for _, item := range late {
					p.t.Logf("PEER conn#%d -> %s %s (delivered right after the send was answered)", connID, item.class, item.id)
					if err := noise.write(p.ctx, item.node); err != nil {
						return
					}
				}
			}()
		}
	}
}

// deliver sends everything held, framed as an offline backlog.
func (p *backlogPeer) deliver(connID int, noise *backlogNoise) []*backlogItem {
	p.mu.Lock()
	items := append([]*backlogItem(nil), p.queue...)
	p.mu.Unlock()
	messages, receipts := 0, 0
	for _, item := range items {
		if item.class == "message" {
			messages++
		} else {
			receipts++
		}
	}
	p.t.Logf("PEER conn#%d -> offline backlog: %d message(s), %d receipt(s)", connID, messages, receipts)
	_ = noise.write(p.ctx, waBinary.Node{Tag: "ib", Content: []waBinary.Node{{Tag: "offline_preview", Attrs: waBinary.Attrs{"count": len(items), "message": messages, "receipt": receipts, "notification": 0, "appdata": 0}}}})
	for _, item := range items {
		p.t.Logf("PEER conn#%d -> %s %s", connID, item.class, item.id)
		if err := noise.write(p.ctx, item.node); err != nil {
			return items
		}
	}
	_ = noise.write(p.ctx, waBinary.Node{Tag: "ib", Content: []waBinary.Node{{Tag: "offline", Attrs: waBinary.Attrs{"count": len(items)}}}})
	return items
}

func backlogCertificate(t *testing.T, root, static *keys.KeyPair) []byte {
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

type backlogNoise struct {
	conn                    *websocket.Conn
	readCipher, writeCipher cipher.AEAD
	readCount, writeCount   uint32
	writeMu                 sync.Mutex
}

// Copy of ownTransportHandshake (internal/wa/own_message_transport_test.go),
// which this package cannot import.
func backlogHandshake(ctx context.Context, conn *websocket.Conn, static *keys.KeyPair, cert []byte) (*backlogNoise, error) {
	_, hello, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(hello, socket.WAConnHeader) {
		return nil, fmt.Errorf("missing WA header")
	}
	hello, err = backlogFrame(hello[len(socket.WAConnHeader):])
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
	if err := backlogWriteFrame(ctx, conn, response); err != nil {
		return nil, err
	}
	_, finish, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	finish, err = backlogFrame(finish)
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
	peer := &backlogNoise{conn: conn}
	if peer.readCipher, err = makeCipher(readKey); err != nil {
		return nil, err
	}
	peer.writeCipher, err = makeCipher(writeKey)
	return peer, err
}

func backlogFrame(message []byte) ([]byte, error) {
	if len(message) < 3 {
		return nil, fmt.Errorf("short frame")
	}
	length := int(message[0])<<16 | int(message[1])<<8 | int(message[2])
	if length != len(message)-3 {
		return nil, fmt.Errorf("frame length %d != %d", length, len(message)-3)
	}
	return message[3:], nil
}

func backlogWriteFrame(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	frame := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	return conn.Write(ctx, websocket.MessageBinary, append(frame, payload...))
}

func backlogIV(count uint32) []byte {
	iv := make([]byte, 12)
	binary.BigEndian.PutUint32(iv[8:], count)
	return iv
}

func (p *backlogNoise) read(ctx context.Context) (*waBinary.Node, error) {
	_, frame, err := p.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	frame, err = backlogFrame(frame)
	if err != nil {
		return nil, err
	}
	plain, err := p.readCipher.Open(nil, backlogIV(p.readCount), frame, nil)
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

func (p *backlogNoise) write(ctx context.Context, node waBinary.Node) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	encoded, err := waBinary.Marshal(node)
	if err != nil {
		return err
	}
	ciphertext := p.writeCipher.Seal(nil, backlogIV(p.writeCount), encoded, nil)
	p.writeCount++
	return backlogWriteFrame(ctx, p.conn, ciphertext)
}

// Issue 580, exit. The stand-in answers the send at once, as WhatsApp does,
// and the backlog is still arriving when `send text --post-send-wait 0` (what
// an automated caller uses) closes its connection: part of it is delivered at
// login and part right after the send is answered. Whatever the command
// acknowledged must be in wacli.db when it exits. A later `sync --once`
// follows, and the test logs any item that is then neither stored nor still
// held by the stand-in, without failing on it: that is the one message
// whatsmeow was still decrypting when the socket closed, which this change
// does not cover (it is reported here so that its rate can be measured; see
// WACLI_BACKLOG_EXIT_REPS).
func TestOneShotExitStoresWhatItAcknowledgedRealTransport(t *testing.T) {
	backlogExitTest(t, func(env *backlogEnv) func(id string, at time.Time) *backlogItem {
		return func(id string, at time.Time) *backlogItem {
			return env.incoming(id, at, backlogText("backlog message "+id))
		}
	})
}

// The same with a group backlog: sender-key messages (skmsg).
func TestOneShotExitStoresWhatItAcknowledgedFromAGroupRealTransport(t *testing.T) {
	backlogExitTest(t, func(env *backlogEnv) func(id string, at time.Time) *backlogItem {
		return env.groupSender(types.NewJID("120363000000000001", types.GroupServer))
	})
}

func backlogExitTest(t *testing.T, sender func(*backlogEnv) func(string, time.Time) *backlogItem) {
	reps := 4
	if n, err := strconv.Atoi(os.Getenv("WACLI_BACKLOG_EXIT_REPS")); err == nil && n > 0 {
		reps = n
	}
	const atLogin, afterSend = 150, 150
	lost := 0
	for rep := 1; rep <= reps; rep++ {
		t.Run(fmt.Sprintf("rep=%d", rep), func(t *testing.T) {
			env := newBacklogEnv(t)
			env.peer.answerAtOnce = true
			send := sender(env)
			t0 := time.Unix(1700000000, 0).UTC()
			var ids []string
			item := func(i int) *backlogItem {
				id := fmt.Sprintf("bl-%04d", i)
				ids = append(ids, id)
				return send(id, t0.Add(time.Duration(i)*time.Second))
			}
			for i := 0; i < atLogin; i++ {
				env.peer.hold(item(i))
			}
			for i := atLogin; i < atLogin+afterSend; i++ {
				env.peer.holdAfterSend(item(i))
			}
			stdout, stderr, err := backlogRunCLI(t, []string{"--store", env.dir, "--json", "send", "text", "--to", env.contact.User, "--message", "sent while the backlog arrives", "--post-send-wait", "0"})
			if err != nil {
				t.Fatalf("send text: %v (stderr %s)", err, stderr)
			}
			backlogSentID(t, stdout) // stdout stays one JSON document
			for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
				if line != "" {
					t.Logf("  one-shot stderr| %s", line)
				}
			}
			rows := env.messages()
			var unstored []string
			for _, acked := range env.peer.acknowledgedItems() {
				if _, ok := rows[acked.id]; !ok {
					unstored = append(unstored, acked.id)
				}
			}
			if len(unstored) > 0 {
				t.Fatalf("acknowledged to the server but not in wacli.db when the command exited: %v", unstored)
			}
			afterOneShot := len(rows)
			if _, _, err := backlogRunCLI(t, []string{"--store", env.dir, "sync", "--once", "--idle-exit", "1s", "--presence-mode", "quiet"}); err != nil {
				t.Fatalf("sync --once: %v", err)
			}
			rows = env.messages()
			held := map[string]bool{}
			for _, id := range env.peer.held() {
				held[id] = true
			}
			var missing []string
			for _, id := range ids {
				if _, ok := rows[id]; !ok && !held[id] {
					missing = append(missing, id)
				}
			}
			t.Logf("RESULT rep=%d stored by the one-shot=%d (incl. its own send) stored after sync=%d still held=%d lost=%d %v", rep, afterOneShot, len(rows), len(held), len(missing), missing)
			if len(missing) > 0 {
				lost++
				t.Logf("lost for good (neither stored nor still queued: the message being decrypted when the one-shot closed its connection): %v", missing)
			}
		})
	}
	t.Logf("SUMMARY %d of %d runs lost a backlog message at exit (not covered by this change)", lost, reps)
}

// groupSender gives the stand-in a group whose sender key the device already
// holds, as after an earlier sender-key distribution: the contact's key,
// addressed by the contact's LID as whatsmeow decrypts it.
func (e *backlogEnv) groupSender(group types.JID) func(id string, at time.Time) *backlogItem {
	e.t.Helper()
	ctx := e.ctx
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(e.t.TempDir(), "gpeer.db"), "_foreign_keys=on"), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = container.Close() })
	sender := container.NewDevice()
	id := e.contact
	sender.ID = &id
	sender.Account = e.device.Account
	if err := sender.Save(ctx); err != nil {
		e.t.Fatal(err)
	}
	name := protocol.NewSenderKeyName(group.String(), backlogContactLID.SignalAddress())
	builder := groups.NewGroupSessionBuilder(sender, waStore.SignalProtobufSerializer)
	distribution, err := builder.Create(ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	// Install the contact's key on the device, on a connection of its own that
	// is closed before any command opens session.db.
	receiver, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(e.dir, "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	device, err := receiver.GetFirstDevice(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	err = groups.NewGroupSessionBuilder(device, waStore.SignalProtobufSerializer).Process(ctx, name, distribution)
	_ = receiver.Close()
	if err != nil {
		e.t.Fatal(err)
	}
	cipher := groups.NewGroupCipher(builder, name, sender)
	return func(msgID string, at time.Time) *backlogItem {
		e.t.Helper()
		plain, err := proto.Marshal(backlogText("group backlog " + msgID))
		if err != nil {
			e.t.Fatal(err)
		}
		encrypted, err := cipher.Encrypt(ctx, append(plain, 1))
		if err != nil {
			e.t.Fatal(err)
		}
		return &backlogItem{id: msgID, class: "message", node: waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{
			"id": msgID, "from": group, "participant": e.contact, "type": "text", "t": at.Unix(),
		}, Content: []waBinary.Node{{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": "skmsg"}, Content: encrypted.SignedSerialize()}}}}
	}
}
