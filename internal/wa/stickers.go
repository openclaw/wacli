package wa

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types/events"
)

type ParsedSticker struct {
	FileHash      string
	DirectPath    string
	MediaKey      []byte
	FileEncSHA256 []byte
	MimeType      string
	Width         uint32
	Height        uint32
	FileLength    uint64
	IsLottie      bool
	IsAvatar      bool
}

type ParsedFavoriteSticker struct {
	Sticker   ParsedSticker
	Favorite  bool
	Timestamp time.Time
}

type ParsedRecentStickerRemoval struct {
	FileHash   string
	LastSentAt time.Time
}

type ParsedHistorySticker struct {
	Sticker    ParsedSticker
	Weight     float32
	LastSentAt time.Time
}

func ParseFavoriteStickerAppState(evt *events.AppState) (ParsedFavoriteSticker, bool) {
	fileHash, ok := stickerMutationFileHash(evt, appstate.IndexFavoriteSticker)
	if !ok {
		return ParsedFavoriteSticker{}, false
	}
	action := evt.GetStickerAction()
	if action == nil || action.IsFavorite == nil {
		return ParsedFavoriteSticker{}, false
	}
	var ts time.Time
	if evt.GetTimestamp() > 0 {
		ts = time.UnixMilli(evt.GetTimestamp()).UTC()
	}
	return ParsedFavoriteSticker{
		Sticker: ParsedSticker{
			FileHash:      fileHash,
			DirectPath:    strings.TrimSpace(action.GetDirectPath()),
			MediaKey:      action.GetMediaKey(),
			FileEncSHA256: action.GetFileEncSHA256(),
			MimeType:      strings.TrimSpace(action.GetMimetype()),
			Width:         action.GetWidth(),
			Height:        action.GetHeight(),
			FileLength:    action.GetFileLength(),
			IsLottie:      action.GetIsLottie(),
			IsAvatar:      action.GetIsAvatarSticker(),
		},
		Favorite:  action.GetIsFavorite(),
		Timestamp: ts,
	}, true
}

func ParseRemoveRecentStickerAppState(evt *events.AppState) (ParsedRecentStickerRemoval, bool) {
	fileHash, ok := stickerMutationFileHash(evt, appstate.IndexRemoveRecentSticker)
	if !ok {
		return ParsedRecentStickerRemoval{}, false
	}
	action := evt.GetRemoveRecentStickerAction()
	if action == nil {
		return ParsedRecentStickerRemoval{}, false
	}
	return ParsedRecentStickerRemoval{
		FileHash:   fileHash,
		LastSentAt: callLogRecordTimestamp(action.GetLastStickerSentTS(), time.Time{}),
	}, true
}

func ParseHistorySticker(meta *waHistorySync.StickerMetadata) (ParsedHistorySticker, bool) {
	if meta == nil || len(meta.GetFileSHA256()) != sha256.Size {
		return ParsedHistorySticker{}, false
	}
	return ParsedHistorySticker{
		Sticker: ParsedSticker{
			FileHash:      base64.StdEncoding.EncodeToString(meta.GetFileSHA256()),
			DirectPath:    strings.TrimSpace(meta.GetDirectPath()),
			MediaKey:      meta.GetMediaKey(),
			FileEncSHA256: meta.GetFileEncSHA256(),
			MimeType:      strings.TrimSpace(meta.GetMimetype()),
			Width:         meta.GetWidth(),
			Height:        meta.GetHeight(),
			FileLength:    meta.GetFileLength(),
			IsLottie:      meta.GetIsLottie(),
			IsAvatar:      meta.GetIsAvatarSticker(),
		},
		Weight:     meta.GetWeight(),
		LastSentAt: callLogRecordTimestamp(meta.GetLastStickerSentTS(), time.Time{}),
	}, true
}

func stickerMutationFileHash(evt *events.AppState, action string) (string, bool) {
	if evt == nil || evt.SyncActionValue == nil || len(evt.Index) < 2 || evt.Index[0] != action {
		return "", false
	}
	fileHash := strings.TrimSpace(evt.Index[1])
	return fileHash, fileHash != ""
}
