package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/gcmutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
	"google.golang.org/protobuf/proto"
)

// Keep transport fake, but exercise the pinned SDK's sender-scoped SQL lookup,
// LID aliases, authenticated decryption, and history envelope parsing together.
type cryptoEditWA struct {
	*fakeWA
	client *whatsmeow.Client
}

func (f *cryptoEditWA) DecryptSecretEncryptedMessage(ctx context.Context, evt *events.Message) (*waE2E.Message, error) {
	return f.client.DecryptSecretEncryptedMessage(ctx, evt)
}

func (f *cryptoEditWA) ParseWebMessage(chat types.JID, msg *waWeb.WebMessageInfo) (*events.Message, error) {
	return f.client.ParseWebMessage(chat, msg)
}

func newCryptoEditWA(t *testing.T) *cryptoEditWA {
	t.Helper()
	ctx := t.Context()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	device := container.NewDevice()
	jid := types.JID{User: "15550000000", Device: 1, Server: types.DefaultUserServer}
	device.ID = &jid
	device.LID = types.JID{User: "100000", Server: types.HiddenUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte("synthetic edit fixture"), AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	return &cryptoEditWA{fakeWA: newFakeWA(), client: whatsmeow.NewClient(device, nil)}
}

func encryptEditFixture(t *testing.T, evt *events.Message, originalSender types.JID, secret []byte, payload *waE2E.Message) {
	t.Helper()
	plain, err := proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope := evt.Message.GetSecretEncryptedMessage()
	info := envelope.GetTargetMessageKey().GetID() + originalSender.ToNonAD().String() + evt.Info.Sender.ToNonAD().String() + string(whatsmeow.EncSecretMessageEdit)
	key := hkdfutil.SHA256(secret, nil, []byte(info), 32)
	envelope.EncIV = bytes.Repeat([]byte{0x42}, 12)
	envelope.EncPayload, err = gcmutil.Encrypt(key, envelope.EncIV, plain, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSecretEditWithSDKCrypto(t *testing.T) {
	for _, history := range []bool{false, true} {
		mode := "live"
		if history {
			mode = "history"
		}
		for _, name := range []string{"group", "dm", "outgoing", "device-sent", "ephemeral", "sender-relative", "lid-alias", "other-author", "forged-from-me", "forged-participant", "bad-ciphertext", "missing-secret", "redirected-chat", "nested-revoke", "target-mismatch", "empty", "context-only"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				ctx := t.Context()
				a := newTestApp(t)
				f := newCryptoEditWA(t)
				a.wa = f
				chat := types.JID{User: "120363000000", Server: types.GroupServer}
				owner := types.JID{User: "15550000001", Server: types.DefaultUserServer}
				editor := owner
				fromMe := false
				if name == "dm" {
					chat = owner
				}
				if name == "outgoing" || name == "device-sent" {
					chat = owner
					owner = f.client.Store.ID.ToNonAD()
					editor = owner
					fromMe = true
				}
				if name == "lid-alias" {
					editor = types.JID{User: "100001", Server: types.HiddenUserServer}
					if err := f.client.Store.LIDs.PutLIDMapping(ctx, editor, owner); err != nil {
						t.Fatal(err)
					}
					f.lids[editor] = owner
				}
				if strings.HasPrefix(name, "forged-") || name == "other-author" {
					editor = types.JID{User: "15550000002", Server: types.DefaultUserServer}
				}
				base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				original := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: owner, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer}, ID: "original-id", Timestamp: base}, Message: &waE2E.Message{Conversation: proto.String("original body")}}
				evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: editor, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer}, ID: "edit-envelope", Timestamp: base.Add(time.Minute)}, Message: secretGroupEditEnvelope(chat, owner, "original-id")}
				target := evt.Message.SecretEncryptedMessage.TargetMessageKey
				if name == "sender-relative" || name == "forged-from-me" || name == "lid-alias" || name == "outgoing" || name == "device-sent" {
					target.FromMe = proto.Bool(true)
				}
				if name == "forged-participant" {
					target.Participant = proto.String(editor.String())
				}
				if name == "redirected-chat" {
					target.RemoteJID = proto.String("120363000001@g.us")
				}
				payload := decryptedProtocolEdit("edited body")
				if name == "nested-revoke" {
					payload = wrappedRevokeProtocolEdit(chat, "original-id")
				}
				if name == "target-mismatch" {
					payload.ProtocolMessage.Key = &waCommon.MessageKey{ID: proto.String("other-id")}
				}
				if name == "empty" {
					payload.ProtocolMessage.EditedMessage = &waE2E.Message{}
				}
				if name == "context-only" {
					payload.ProtocolMessage.EditedMessage = &waE2E.Message{MessageContextInfo: &waE2E.MessageContextInfo{}}
				}
				secret := bytes.Repeat([]byte{0x23}, 32)
				if name != "missing-secret" {
					if err := f.client.Store.MsgSecrets.PutMessageSecret(ctx, chat, owner, "original-id", secret); err != nil {
						t.Fatal(err)
					}
				}
				// Forgers know the shared secret and can choose every header/key input.
				// The victim's SQL lookup must still refuse their claimed ownership.
				cryptoOwner := owner
				if strings.HasPrefix(name, "forged-") {
					cryptoOwner = editor
				}
				encryptEditFixture(t, evt, cryptoOwner, secret, payload)
				if name == "bad-ciphertext" {
					evt.Message.SecretEncryptedMessage.EncPayload[0] ^= 1
				}
				_, decryptErr := f.client.DecryptSecretEncryptedMessage(ctx, evt)
				wantDecryptFailure := strings.HasPrefix(name, "forged-") || name == "bad-ciphertext" || name == "missing-secret"
				if (decryptErr != nil) != wantDecryptFailure {
					t.Fatalf("SDK decrypt error = %v, want failure %t", decryptErr, wantDecryptFailure)
				}
				accepted := name == "device-sent" || name == "ephemeral" || name == "group" || name == "dm" || name == "outgoing" || name == "sender-relative" || name == "lid-alias"
				if name == "device-sent" {
					evt.Message = &waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{DestinationJID: proto.String(chat.String()), Message: evt.Message}}
				} else if name == "ephemeral" {
					evt.Message = &waE2E.Message{EphemeralMessage: &waE2E.FutureProofMessage{Message: evt.Message}}
				}
				if !history {
					evt.RawMessage = evt.Message
					evt.UnwrapRaw()
				}
				var stored, lastEvent atomic.Int64
				webhooks := 0
				out := captureStderr(t, func() {
					if history {
						toWeb := func(msg *events.Message) *waHistorySync.HistorySyncMsg {
							return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), Participant: proto.String(msg.Info.Sender.String()), FromMe: proto.Bool(msg.Info.IsFromMe), ID: proto.String(msg.Info.ID)}, MessageTimestamp: proto.Uint64(uint64(msg.Info.Timestamp.Unix())), Message: msg.Message}}
						}
						// Histories may be newest-first: authorization cannot depend on a local row.
						a.handleHistorySync(ctx, SyncOptions{}, &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{ID: proto.String(chat.String()), Messages: []*waHistorySync.HistorySyncMsg{toWeb(evt), toWeb(original)}}}}}, &stored, &lastEvent, func(string, string) {})
					} else {
						a.handleLiveSyncMessage(ctx, SyncOptions{}, original, &stored, func(string, string) {}, nil)
						a.handleLiveSyncMessage(ctx, SyncOptions{}, evt, &stored, func(string, string) {}, func(wa.ParsedMessage) { webhooks++ })
					}
				})
				msg, err := a.db.GetMessage(chat.String(), "original-id")
				if err != nil {
					t.Fatal(err)
				}
				wantText := "original body"
				wantStored := int64(1)
				if accepted {
					wantText = "edited body"
					wantStored = 2
				}
				if msg.Text != wantText || msg.Edited != accepted || msg.Revoked || msg.FromMe != fromMe || msg.SenderJID != owner.String() || !msg.Timestamp.Equal(base) {
					t.Fatalf("unexpected original after %s: %+v; diagnostics: %s", name, msg, out)
				}
				if stored.Load() != wantStored {
					t.Fatalf("stored=%d, want %d; diagnostics: %s", stored.Load(), wantStored, out)
				}
				if n, err := a.db.CountMessages(); err != nil || n != 1 {
					t.Fatalf("row count=%d err=%v", n, err)
				}
				wantWebhooks := 0
				if accepted && !history {
					wantWebhooks = 1
				}
				if webhooks != wantWebhooks {
					t.Fatalf("webhooks=%d, want %d", webhooks, wantWebhooks)
				}
				if !accepted && !strings.Contains(out, "warning:") {
					t.Fatalf("rejection without warning: %s", out)
				}
			})
		}
	}
}
