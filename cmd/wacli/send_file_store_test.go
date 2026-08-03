package main

import (
	"context"
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
	sentID types.MessageID
}

func (f *fakeSendFileWA) Upload(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, nil
}

func (f *fakeSendFileWA) SendProtoMessage(context.Context, types.JID, *waProto.Message) (types.MessageID, error) {
	return f.sentID, nil
}

func (f *fakeSendFileWA) ResolveChatName(context.Context, types.JID, string) string {
	return "Test Chat"
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
