package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// minimalStickerWebP builds the smallest VP8L container that passes
// validateWebPSticker: a 512x512 static lossless header with no pixel data.
func minimalStickerWebP(t *testing.T) string {
	t.Helper()
	chunk := []byte{0x2f, 0, 0, 0, 0}
	// 14-bit width-1 and height-1 packed little-endian: 511 | 511<<14.
	binary.LittleEndian.PutUint32(chunk[1:5], 511|511<<14)
	payload := append([]byte("WEBPVP8L"), 5, 0, 0, 0)
	payload = append(payload, chunk...)
	payload = append(payload, 0) // odd chunk size padding
	data := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(payload)))
	data = append(data, payload...)

	path := filepath.Join(t.TempDir(), "sticker.webp")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write sticker: %v", err)
	}
	return path
}

func TestSendStickerPersistsHistory(t *testing.T) {
	a, db, _ := newSendFileFixture(t)
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendSticker(context.Background(), a, to, minimalStickerWebP(t), sendStickerOptions{})
	if err != nil {
		t.Fatalf("sendSticker: %v", err)
	}
	if res.id != "MSG1" || res.storeWarning != nil {
		t.Fatalf("expected clean outcome, got %+v", res)
	}
	msg, err := db.GetMessage(to.String(), "MSG1")
	if err != nil {
		t.Fatalf("sent sticker not persisted: %v", err)
	}
	if msg.MediaType != "sticker" {
		t.Fatalf("unexpected persisted message: %+v", msg)
	}
}

func TestSendStickerStoreFailureKeepsDeliveredID(t *testing.T) {
	a, db, _ := newSendFileFixture(t)
	path := minimalStickerWebP(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendSticker(context.Background(), a, to, path, sendStickerOptions{})
	if err != nil {
		t.Fatalf("store failure must not fail the send, got %v", err)
	}
	if res.id != "MSG1" || res.storeWarning == nil {
		t.Fatalf("expected delivered ID with store warning, got %+v", res)
	}
}

func TestUpsertSentReactionSurfacesStoreFailure(t *testing.T) {
	_, db, _ := newSendFileFixture(t)
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	now := time.Now().UTC()

	if err := upsertSentReaction(db, chat, "Chat", "R1", "T1", "👍", now); err != nil {
		t.Fatalf("healthy store: %v", err)
	}
	if _, err := db.GetMessage(chat.String(), "R1"); err != nil {
		t.Fatalf("sent reaction not persisted: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := upsertSentReaction(db, chat, "Chat", "R2", "T1", "👍", now); err == nil {
		t.Fatal("expected store error after close")
	}
}

type fakeTextResolver struct{}

func (fakeTextResolver) ResolveChatName(context.Context, types.JID, string) string { return "Chat" }
func (fakeTextResolver) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID { return jid }

func TestPersistOutboundTextSurfacesStoreFailure(t *testing.T) {
	_, db, _ := newSendFileFixture(t)
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	now := time.Now().UTC()

	if err := persistOutboundTextWith(context.Background(), db, fakeTextResolver{}, chat, "TX1", "hello", now); err != nil {
		t.Fatalf("healthy store: %v", err)
	}
	if _, err := db.GetMessage(chat.String(), "TX1"); err != nil {
		t.Fatalf("sent text not persisted: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := persistOutboundTextWith(context.Background(), db, fakeTextResolver{}, chat, "TX2", "hello", now); err == nil {
		t.Fatal("expected store error after close")
	}
}
