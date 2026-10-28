package wa

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func favoriteStickerEvent(index []string, action *waSyncAction.StickerAction, ts time.Time) *events.AppState {
	return &events.AppState{Index: index, SyncActionValue: &waSyncAction.SyncActionValue{
		Timestamp:     proto.Int64(ts.UnixMilli()),
		StickerAction: action,
	}}
}

func TestParseFavoriteStickerAppStateBothDirections(t *testing.T) {
	digest := sha256.Sum256([]byte("sticker"))
	fileHash := base64.StdEncoding.EncodeToString(digest[:])
	ts := time.Date(2026, 10, 1, 12, 0, 0, 500_000_000, time.UTC)
	for _, favorite := range []bool{true, false} {
		evt := favoriteStickerEvent([]string{"favoriteSticker", fileHash}, &waSyncAction.StickerAction{
			DirectPath:      proto.String("/v/t62/sticker"),
			MediaKey:        []byte{1, 2, 3},
			FileEncSHA256:   []byte{4, 5, 6},
			Mimetype:        proto.String("image/webp"),
			Width:           proto.Uint32(512),
			Height:          proto.Uint32(512),
			FileLength:      proto.Uint64(4096),
			IsFavorite:      proto.Bool(favorite),
			IsLottie:        proto.Bool(true),
			IsAvatarSticker: proto.Bool(true),
		}, ts)
		got, ok := ParseFavoriteStickerAppState(evt)
		if !ok {
			t.Fatalf("favorite=%v: not parsed", favorite)
		}
		s := got.Sticker
		if got.Favorite != favorite || !got.Timestamp.Equal(ts) || s.FileHash != fileHash ||
			s.DirectPath != "/v/t62/sticker" || !bytes.Equal(s.MediaKey, []byte{1, 2, 3}) ||
			!bytes.Equal(s.FileEncSHA256, []byte{4, 5, 6}) || s.MimeType != "image/webp" ||
			s.Width != 512 || s.Height != 512 || s.FileLength != 4096 || !s.IsLottie || !s.IsAvatar {
			t.Fatalf("favorite=%v: parsed %+v", favorite, got)
		}
	}
}

func TestParseFavoriteStickerAppStateRejectsMalformedMutations(t *testing.T) {
	action := &waSyncAction.StickerAction{IsFavorite: proto.Bool(true)}
	now := time.Now()
	for name, evt := range map[string]*events.AppState{
		"nil":                nil,
		"missing hash":       favoriteStickerEvent([]string{"favoriteSticker"}, action, now),
		"blank hash":         favoriteStickerEvent([]string{"favoriteSticker", " "}, action, now),
		"other action index": favoriteStickerEvent([]string{"removeRecentSticker", "hash"}, action, now),
		"no sticker action":  favoriteStickerEvent([]string{"favoriteSticker", "hash"}, nil, now),
		// WhatsApp Web treats a sticker action without isFavorite as malformed.
		"no isFavorite": favoriteStickerEvent([]string{"favoriteSticker", "hash"}, &waSyncAction.StickerAction{DirectPath: proto.String("/p")}, now),
		"call log": {Index: []string{"call_log", "id"}, SyncActionValue: &waSyncAction.SyncActionValue{
			CallLogAction: &waSyncAction.CallLogAction{},
		}},
	} {
		if got, ok := ParseFavoriteStickerAppState(evt); ok {
			t.Fatalf("%s: parsed %+v", name, got)
		}
	}
}

func TestParseRemoveRecentStickerAppState(t *testing.T) {
	sentAt := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	evt := &events.AppState{Index: []string{"removeRecentSticker", "hash"}, SyncActionValue: &waSyncAction.SyncActionValue{
		RemoveRecentStickerAction: &waSyncAction.RemoveRecentStickerAction{LastStickerSentTS: proto.Int64(sentAt.UnixMilli())},
	}}
	got, ok := ParseRemoveRecentStickerAppState(evt)
	if !ok || got.FileHash != "hash" || !got.LastSentAt.Equal(sentAt) {
		t.Fatalf("removal = %+v, %v", got, ok)
	}
	if _, ok := ParseRemoveRecentStickerAppState(favoriteStickerEvent([]string{"favoriteSticker", "hash"}, &waSyncAction.StickerAction{IsFavorite: proto.Bool(true)}, sentAt)); ok {
		t.Fatal("favorite mutation parsed as a recent removal")
	}
}

func TestParseHistoryStickerUsesAppStateIdentity(t *testing.T) {
	digest := sha256.Sum256([]byte("sticker"))
	sentAt := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	got, ok := ParseHistorySticker(&waHistorySync.StickerMetadata{
		FileSHA256:        digest[:],
		FileEncSHA256:     []byte{4, 5, 6},
		MediaKey:          []byte{1, 2, 3},
		DirectPath:        proto.String("/v/t62/sticker"),
		Mimetype:          proto.String("image/webp"),
		Weight:            proto.Float32(2.5),
		LastStickerSentTS: proto.Int64(sentAt.Unix()),
	})
	if !ok {
		t.Fatal("history sticker not parsed")
	}
	if got.Sticker.FileHash != base64.StdEncoding.EncodeToString(digest[:]) {
		t.Fatalf("file hash = %q, want the base64 file digest app state indexes by", got.Sticker.FileHash)
	}
	if got.Weight != 2.5 || !got.LastSentAt.Equal(sentAt) || got.Sticker.DirectPath != "/v/t62/sticker" {
		t.Fatalf("history sticker = %+v", got)
	}
	if _, ok := ParseHistorySticker(&waHistorySync.StickerMetadata{FileSHA256: []byte{1}}); ok {
		t.Fatal("history sticker without a full digest parsed")
	}
	if _, ok := ParseHistorySticker(nil); ok {
		t.Fatal("nil history sticker parsed")
	}
}
