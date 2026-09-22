package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveMediaWebhookOmitsRetrievalMaterial(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		bodies <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	chat := types.NewJID("15551234567", types.DefaultUserServer)
	image := &waProto.ImageMessage{
		Caption: proto.String("synthetic caption"), Mimetype: proto.String("image/jpeg"),
		DirectPath: proto.String("/synthetic-media"), MediaKey: []byte("synthetic-key"),
		FileSHA256: []byte("synthetic-hash"), FileEncSHA256: []byte("synthetic-encrypted-hash"),
		FileLength: proto.Uint64(123),
	}
	evt := &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "media-test", Timestamp: time.Now()},
		Message: &waProto.Message{ImageMessage: image},
	}
	var stored atomic.Int64
	var original *wa.Media
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &stored, func(string, string) {}, func(pm wa.ParsedMessage) {
		original = pm.Media
		if err := a.postSyncWebhookEvent(context.Background(), SyncOptions{WebhookURL: srv.URL, WebhookAllowPrivate: true}, syncWebhookEvent{Message: pm}); err != nil {
			t.Fatal(err)
		}
	})
	if stored.Load() != 1 || original == nil {
		t.Fatal("media message was not stored and forwarded")
	}
	var payload struct{ Media map[string]any }
	if err := json.Unmarshal(<-bodies, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"Type": "image", "Caption": "synthetic caption", "Filename": "", "MimeType": "image/jpeg", "FileLength": float64(123)}
	if !reflect.DeepEqual(payload.Media, want) {
		t.Errorf("webhook Media = %#v, want metadata only: %#v", payload.Media, want)
	}
	if original.DirectPath != image.GetDirectPath() || !bytes.Equal(original.MediaKey, image.MediaKey) || !bytes.Equal(original.FileSHA256, image.FileSHA256) || !bytes.Equal(original.FileEncSHA256, image.FileEncSHA256) {
		t.Error("webhook serialization mutated the source media")
	}
	info, err := a.db.GetMediaDownloadInfo(chat.String(), evt.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.DirectPath != image.GetDirectPath() || !bytes.Equal(info.MediaKey, image.MediaKey) || !bytes.Equal(info.FileSHA256, image.FileSHA256) || !bytes.Equal(info.FileEncSHA256, image.FileEncSHA256) {
		t.Error("local download material was not preserved")
	}
}
