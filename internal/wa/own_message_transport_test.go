package wa_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

// Real App.Sync -> whatsmeow -> Noise XX/WebSocket -> in-process peer. Unlike
// handler injection, both incoming and own-device messages cross the socket and
// Signal decryption before the runtime persists them and the SDK sends its
// delivery/sender receipt (acknowledged by the peer).
// Pairing/login and the remote service's read policy are outside this proof.
func TestOwnReplyRealTransport(t *testing.T) {
	// The exported SDK trust root is process-wide: never run this in parallel.
	root := keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
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
	static := keys.NewKeyPair()
	cert := ownTransportCertificate(t, root, static)
	peers := make(chan *ownTransportNoise, 1)
	receipts := make(chan string, 32)
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
		t.Log("NOISE XX: ConnectContext verified test-CA certificate; synthetic paired device")
		peers <- peer
		for {
			node, err := peer.read(ctx)
			if err != nil {
				return
			}
			t.Logf("PEER received tag=%s id=%v type=%v", node.Tag, node.Attrs["id"], node.Attrs["type"])
			if node.Tag == "receipt" {
				kind := fmt.Sprint(node.Attrs["type"])
				if kind != "inactive" && kind != "sender" {
					t.Errorf("unexpected receipt type %s: automatic acknowledgement must stay local", kind)
				}
				if err := peer.write(ctx, waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": node.Attrs["id"], "class": "receipt", "type": node.Attrs["type"], "from": node.Attrs["to"]}}); err != nil {
					return
				}
				receipts <- fmt.Sprint(node.Attrs["id"])
			}
			if node.Tag == "iq" {
				// Empty collections keep bootstrap offline. This peer does not
				// claim to emulate the production app-state service.
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
	chat := types.NewJID("5551230000", types.DefaultUserServer)
	incoming := ownTransportSender(t, ctx, cli.Store, chat)
	otherDevice := *cli.Store.ID
	otherDevice.Device = 1
	own := ownTransportSender(t, ctx, cli.Store, otherDevice)
	base := time.Unix(1700000000, 0)

	_, err = a.Sync(ctx, app.SyncOptions{Mode: app.SyncModeOnce, IdleExit: 20 * time.Millisecond, PresenceMode: app.SyncPresenceModeQuiet, AfterConnect: func(ctx context.Context) error {
		seen := make(chan string, 16)
		observer := cli.AddEventHandler(func(evt any) {
			if msg, ok := evt.(*events.Message); ok {
				seen <- msg.Info.ID
			}
		})
		defer cli.RemoveEventHandler(observer)
		send := func(id string, fromMe bool, at time.Time) {
			t.Helper()
			makeNode := incoming
			if fromMe {
				makeNode = own
			}
			if err := peer.write(ctx, makeNode(id, chat, at)); err != nil {
				t.Fatal(err)
			}
			for _, ch := range []<-chan string{seen, receipts} {
				select {
				case got := <-ch:
					if got != id {
						t.Fatalf("runtime/receipt ID = %s, want %s", got, id)
					}
				case <-ctx.Done():
					t.Fatal("runtime did not persist/receipt message")
				}
			}
		}
		state := func(label string, unread bool, count int) {
			t.Helper()
			c, err := a.DB().GetChat(chat.String())
			if err != nil || c.Unread != unread || c.UnreadCount != count {
				t.Fatalf("%s: unread=(%v,%d), error=%v; want (%v,%d)", label, c.Unread, c.UnreadCount, err, unread, count)
			}
			t.Logf("STORE %s: unread=%v unread_count=%d (after SDK event and wire delivery/sender receipt)", label, c.Unread, c.UnreadCount)
		}
		send("incoming", false, base)
		state("before own reply", true, 1)
		send("own", true, base.Add(time.Second))
		state("after own reply", false, 0)
		if err := a.DB().SetChatUnread(chat.String(), true); err != nil {
			return err
		}
		send("own", true, base.Add(time.Second))
		state("replay preserves newer manual marker", true, 0)
		send("same-second-before", false, base.Add(2*time.Second))
		state("incoming before boundary", true, 1)
		send("own-fresh", true, base.Add(2*time.Second))
		state("fresh identity covers earlier same-second arrival", false, 0)
		send("same-second-after", false, base.Add(2*time.Second))
		state("arrival beyond own reply", true, 1)
		send("own-fresh", true, base.Add(2*time.Second))
		state("replay retains later same-second arrival", true, 1)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func ownTransportSender(t *testing.T, ctx context.Context, receiver *waStore.Device, from types.JID) func(string, types.JID, time.Time) waBinary.Node {
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
	return func(id string, chat types.JID, at time.Time) waBinary.Node {
		t.Helper()
		plain, err := proto.Marshal(&waE2E.Message{Conversation: proto.String("synthetic transport proof")})
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

// Noise responder adapted from the qr-only regular_transport_test.go proof.
func ownTransportCertificate(t *testing.T, root, static *keys.KeyPair) []byte {
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

type ownTransportNoise struct {
	conn                    *websocket.Conn
	readCipher, writeCipher cipher.AEAD
	readCount, writeCount   uint32
	writeMu                 sync.Mutex
}

func ownTransportHandshake(ctx context.Context, conn *websocket.Conn, static *keys.KeyPair, cert []byte) (*ownTransportNoise, error) {
	_, hello, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(hello, socket.WAConnHeader) {
		return nil, fmt.Errorf("missing WA header")
	}
	hello, err = ownTransportFrame(hello[len(socket.WAConnHeader):])
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
	if err := ownTransportWriteFrame(ctx, conn, response); err != nil {
		return nil, err
	}
	_, finish, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	finish, err = ownTransportFrame(finish)
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
	peer := &ownTransportNoise{conn: conn}
	if peer.readCipher, err = makeCipher(readKey); err != nil {
		return nil, err
	}
	peer.writeCipher, err = makeCipher(writeKey)
	return peer, err
}

func ownTransportFrame(message []byte) ([]byte, error) {
	if len(message) < 3 {
		return nil, fmt.Errorf("short frame")
	}
	length := int(message[0])<<16 | int(message[1])<<8 | int(message[2])
	if length != len(message)-3 {
		return nil, fmt.Errorf("frame length %d != %d", length, len(message)-3)
	}
	return message[3:], nil
}

func ownTransportWriteFrame(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	frame := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	return conn.Write(ctx, websocket.MessageBinary, append(frame, payload...))
}

func ownTransportIV(count uint32) []byte {
	iv := make([]byte, 12)
	binary.BigEndian.PutUint32(iv[8:], count)
	return iv
}

func (p *ownTransportNoise) read(ctx context.Context) (*waBinary.Node, error) {
	_, frame, err := p.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	frame, err = ownTransportFrame(frame)
	if err != nil {
		return nil, err
	}
	plain, err := p.readCipher.Open(nil, ownTransportIV(p.readCount), frame, nil)
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

func (p *ownTransportNoise) write(ctx context.Context, node waBinary.Node) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	encoded, err := waBinary.Marshal(node)
	if err != nil {
		return err
	}
	ciphertext := p.writeCipher.Seal(nil, ownTransportIV(p.writeCount), encoded, nil)
	p.writeCount++
	return ownTransportWriteFrame(ctx, p.conn, ciphertext)
}
