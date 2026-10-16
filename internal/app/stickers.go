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

func (a *App) handleStickerAppStateEvent(evt *events.AppState) error {
	if favorite, ok := wa.ParseFavoriteStickerAppState(evt); ok {
		err := a.db.SetFavoriteSticker(store.SetFavoriteStickerParams{
			FileHash:  favorite.Sticker.FileHash,
			Media:     stickerMedia(favorite.Sticker),
			Favorite:  favorite.Favorite,
			ChangedAt: favorite.Timestamp,
		})
		if err != nil {
			a.emitWarning(
				"favorite_sticker_store_failed",
				fmt.Sprintf("warning: failed to store favorite sticker %s: %v", favorite.Sticker.FileHash, err),
				map[string]any{"file_hash": favorite.Sticker.FileHash, "error": err.Error()},
			)
		}
		return err
	}
	removal, ok := wa.ParseRemoveRecentStickerAppState(evt)
	if !ok {
		return nil
	}
	if err := a.db.RemoveRecentSticker(removal.FileHash, removal.LastSentAt); err != nil {
		a.emitWarning(
			"recent_sticker_remove_failed",
			fmt.Sprintf("warning: failed to remove recent sticker %s: %v", removal.FileHash, err),
			map[string]any{"file_hash": removal.FileHash, "error": err.Error()},
		)
		return err
	}
	return nil
}

func (a *App) storeHistoryStickers(v *events.HistorySync, lastEvent *atomic.Int64) {
	if v == nil || v.Data == nil {
		return
	}
	for _, meta := range v.Data.GetFavoriteStickers() {
		lastEvent.Store(nowUTC().UnixNano())
		if sticker, ok := wa.ParseHistorySticker(meta); ok {
			a.storeHistorySticker(store.UpsertHistoryStickerParams{
				FileHash: sticker.Sticker.FileHash,
				Media:    stickerMedia(sticker.Sticker),
				Favorite: true,
			})
		}
	}
	for _, meta := range v.Data.GetRecentStickers() {
		lastEvent.Store(nowUTC().UnixNano())
		if sticker, ok := wa.ParseHistorySticker(meta); ok {
			a.storeHistorySticker(store.UpsertHistoryStickerParams{
				FileHash:   sticker.Sticker.FileHash,
				Media:      stickerMedia(sticker.Sticker),
				Recent:     true,
				Weight:     sticker.Weight,
				LastSentAt: sticker.LastSentAt,
			})
		}
	}
}

func (a *App) storeHistorySticker(p store.UpsertHistoryStickerParams) {
	if err := a.db.UpsertHistorySticker(p); err != nil {
		a.emitWarning(
			"history_sticker_store_failed",
			fmt.Sprintf("warning: failed to store history sticker %s: %v", p.FileHash, err),
			map[string]any{"file_hash": p.FileHash, "error": err.Error()},
		)
	}
}

func stickerMedia(s wa.ParsedSticker) store.StickerMedia {
	return store.StickerMedia{
		DirectPath:    s.DirectPath,
		MediaKey:      s.MediaKey,
		FileEncSHA256: s.FileEncSHA256,
		FileLength:    s.FileLength,
		MimeType:      s.MimeType,
		Width:         s.Width,
		Height:        s.Height,
		IsLottie:      s.IsLottie,
		IsAvatar:      s.IsAvatar,
	}
}
