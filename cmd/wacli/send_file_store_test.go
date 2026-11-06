package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// fakeSendFileWA stubs only the methods sendFile touches; any other call
// panics through the embedded nil interface, catching accidental coupling.
type fakeSendFileWA struct {
	app.WAClient
	sentID     types.MessageID
	mappedChat types.JID
	wireTarget types.JID
}

func (f *fakeSendFileWA) Upload(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	// Preserve real content identity across the fake upload/store boundary.
	hash := sha256.Sum256(data)
	return whatsmeow.UploadResponse{FileSHA256: hash[:], FileLength: uint64(len(data))}, nil
}

func (f *fakeSendFileWA) SendProtoMessage(_ context.Context, to types.JID, _ *waProto.Message) (types.MessageID, error) {
	f.wireTarget = to
	return f.sentID, nil
}

func (f *fakeSendFileWA) ResolveChatName(context.Context, types.JID, string) string {
	return "Test Chat"
}

func (f *fakeSendFileWA) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	if !f.mappedChat.IsEmpty() {
		return f.mappedChat
	}
	return jid
}

// Quoted replies look the target up under the chat's phone/LID alias and read
// the linked account; model an unmapped phone and an unknown account.
func (f *fakeSendFileWA) ResolvePNToLID(_ context.Context, jid types.JID) types.JID {
	return jid
}

func (f *fakeSendFileWA) LinkedJID() string {
	return ""
}

type fakeSendFileApp struct {
	wa app.WAClient
	db *store.DB
}

func (f *fakeSendFileApp) WA() app.WAClient { return f.wa }
func (f *fakeSendFileApp) DB() *store.DB    { return f.db }

func newSendFileFixture(t *testing.T) (*fakeSendFileApp, *store.DB, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	filePath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	a := &fakeSendFileApp{wa: &fakeSendFileWA{sentID: "MSG1"}, db: db}
	return a, db, filePath
}

func TestSendFilePersistsHistory(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendFile(context.Background(), a, to, filePath, sendFileOptions{caption: "hi"})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if res.id != "MSG1" || res.storeWarning != nil {
		t.Fatalf("expected clean outcome, got %+v", res)
	}
	msg, err := db.GetMessage(to.String(), "MSG1")
	if err != nil {
		t.Fatalf("sent message not persisted: %v", err)
	}
	if !msg.FromMe || msg.Text != "hi" {
		t.Fatalf("unexpected persisted message: %+v", msg)
	}
}

func TestSendFilePersistsCanonicalRecipient(t *testing.T) {
	phone := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("20001", types.HiddenUserServer)
	for _, tc := range []struct {
		name               string
		to, mapped, stored types.JID
	}{
		{name: "mapped LID", to: lid, mapped: phone, stored: phone},
		{name: "unknown LID", to: lid, stored: lid},
		{name: "phone", to: phone, stored: phone},
		{name: "group", to: types.NewJID("123-456", types.GroupServer), stored: types.NewJID("123-456", types.GroupServer)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, db, filePath := newSendFileFixture(t)
			fake := a.wa.(*fakeSendFileWA)
			fake.mappedChat = tc.mapped
			res, err := sendFile(context.Background(), a, tc.to, filePath, sendFileOptions{caption: "caption"})
			if err != nil || res.storeWarning != nil || res.id != "MSG1" {
				t.Fatalf("accepted outcome = %+v, err %v", res, err)
			}
			if fake.wireTarget != tc.to {
				t.Fatalf("wire target = %s, want %s", fake.wireTarget, tc.to)
			}
			message, err := db.GetMessage(tc.stored.String(), res.id)
			if err != nil || message.MediaCaption != "caption" || !message.FromMe {
				t.Fatalf("exact canonical attachment history = %+v, err %v", message, err)
			}
			if _, err := db.GetChat(tc.stored.String()); err != nil {
				t.Fatalf("canonical chat missing: %v", err)
			}
			media, err := db.GetMediaDownloadInfo(tc.stored.String(), res.id)
			hash := sha256.Sum256([]byte("hello"))
			if err != nil || !bytes.Equal(media.FileSHA256, hash[:]) || media.FileLength != 5 {
				t.Fatalf("accepted attachment identity = %+v, err %v", media, err)
			}
			// A mapped transport alias must not create a sibling message or chat.
			if tc.stored != tc.to {
				if _, err := db.GetMessage(tc.to.String(), res.id); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("transport sibling message lookup = %v", err)
				}
				if _, err := db.GetChat(tc.to.String()); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("transport sibling chat lookup = %v", err)
				}
			}
		})
	}
}

// Regression for the silently-swallowed store failures: a delivered message
// whose local persistence fails must keep the ID and surface a warning, not
// return a hard error that invites callers to retry an already-sent message.
func TestSendFileStoreFailureKeepsDeliveredID(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendFile(context.Background(), a, to, filePath, sendFileOptions{})
	if err != nil {
		t.Fatalf("store failure must not fail the send, got %v", err)
	}
	if res.id != "MSG1" {
		t.Fatalf("delivered ID lost, got %+v", res)
	}
	if res.storeWarning == nil {
		t.Fatal("expected store warning for failed history update")
	}
}

func TestSendFileStatusStoreFailureKeepsDeliveredID(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	res, err := sendFile(context.Background(), a, types.StatusBroadcastJID, filePath, sendFileOptions{})
	if err != nil {
		t.Fatalf("store failure must not fail the send, got %v", err)
	}
	if res.id != "MSG1" || res.storeWarning == nil {
		t.Fatalf("expected delivered ID with store warning, got %+v", res)
	}
}

func TestSendFileStatusPersistsHistory(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)

	res, err := sendFile(context.Background(), a, types.StatusBroadcastJID, filePath, sendFileOptions{caption: "st"})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if res.storeWarning != nil {
		t.Fatalf("unexpected store warning: %v", res.storeWarning)
	}
	if _, err := db.GetStatusMessage("MSG1"); err != nil {
		t.Fatalf("status message not persisted: %v", err)
	}
}
