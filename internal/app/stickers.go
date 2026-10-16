package app

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types/events"
)

// Favorites saved before this store recorded them only arrive again in a full
// replay. The durable intent outlives a failed attempt; sync start retries it.
func (a *App) RefreshFavoriteStickers(ctx context.Context) error {
	collection := appstate.WAPatchRegularLow
	if err := a.db.MarkAppStateRecoveryRequired(string(collection)); err != nil {
		return fmt.Errorf("request favorite sticker replay: %w", err)
	}
	release, err := a.beginChatStateWrite(ctx, collection)
	if err != nil {
		return err
	}
	release()
	return nil
}

func (a *App) handleStickerAppStateEvent(evt *events.AppState) (handled bool, err error) {
	if favorite, ok := wa.ParseFavoriteStickerAppState(evt); ok {
		if err := a.db.SetFavoriteSticker(stickerParams(favorite.Sticker), favorite.Favorite, favorite.Timestamp); err != nil {
			a.emitWarning(
				"favorite_sticker_store_failed",
				fmt.Sprintf("warning: failed to store favorite sticker %s: %v", favorite.Sticker.FileHash, err),
				map[string]any{"file_hash": favorite.Sticker.FileHash, "error": err.Error()},
			)
			return true, err
		}
		return true, nil
	}
	if removal, ok := wa.ParseRemoveRecentStickerAppState(evt); ok {
		if err := a.db.RemoveRecentSticker(removal.FileHash, removal.LastSentAt); err != nil {
			a.emitWarning(
				"recent_sticker_remove_failed",
				fmt.Sprintf("warning: failed to remove recent sticker %s: %v", removal.FileHash, err),
				map[string]any{"file_hash": removal.FileHash, "error": err.Error()},
			)
			return true, err
		}
		return true, nil
	}
	return false, nil
}

func (a *App) storeHistoryStickers(v *events.HistorySync, lastEvent *atomic.Int64) {
	if v == nil || v.Data == nil {
		return
	}
	for _, meta := range v.Data.GetFavoriteStickers() {
		lastEvent.Store(nowUTC().UnixNano())
		sticker, ok := wa.ParseHistorySticker(meta)
		if !ok {
			continue
		}
		if err := a.db.AddHistoryFavoriteSticker(stickerParams(sticker.Sticker)); err != nil {
			a.warnHistoryStickerStoreFailed(sticker.Sticker.FileHash, err)
		}
	}
	for _, meta := range v.Data.GetRecentStickers() {
		lastEvent.Store(nowUTC().UnixNano())
		sticker, ok := wa.ParseHistorySticker(meta)
		if !ok {
			continue
		}
		if err := a.db.AddHistoryRecentSticker(stickerParams(sticker.Sticker), sticker.Weight, sticker.LastSentAt); err != nil {
			a.warnHistoryStickerStoreFailed(sticker.Sticker.FileHash, err)
		}
	}
}

func (a *App) warnHistoryStickerStoreFailed(fileHash string, err error) {
	a.emitWarning(
		"history_sticker_store_failed",
		fmt.Sprintf("warning: failed to store history sticker %s: %v", fileHash, err),
		map[string]any{"file_hash": fileHash, "error": err.Error()},
	)
}

func stickerParams(s wa.ParsedSticker) store.StickerParams {
	return store.StickerParams{
		FileHash:      s.FileHash,
		DirectPath:    s.DirectPath,
		MediaKey:      s.MediaKey,
		FileEncSHA256: s.FileEncSHA256,
		MimeType:      s.MimeType,
		Width:         s.Width,
		Height:        s.Height,
		FileLength:    s.FileLength,
		IsLottie:      s.IsLottie,
		IsAvatar:      s.IsAvatar,
	}
}
