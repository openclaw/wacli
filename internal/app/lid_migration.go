package app

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

func (a *App) migrateHistoricalLIDs(ctx context.Context) error {
	if a == nil || a.db == nil || a.wa == nil {
		return nil
	}
	lids, err := a.db.HistoricalLIDJIDs()
	if err != nil {
		return fmt.Errorf("load historical LID rows: %w", err)
	}
	for _, raw := range lids {
		lid, err := types.ParseJID(strings.TrimSpace(raw))
		if err != nil || lid.Server != types.HiddenUserServer {
			continue
		}
		pn := a.wa.ResolveLIDToPN(ctx, lid)
		if pn.IsEmpty() || pn.Server != types.DefaultUserServer {
			continue
		}
		pnJID := canonicalJIDString(pn)
		pendingMedia, err := a.db.LIDMigrationPurgedMedia(raw, pnJID)
		if err != nil {
			return fmt.Errorf("load purged alias media for historical LID %s: %w", raw, err)
		}
		for start := 0; start < len(pendingMedia); {
			end := start + 1
			for end < len(pendingMedia) && pendingMedia[end].ChatJID == pendingMedia[start].ChatJID && pendingMedia[end].MsgID == pendingMedia[start].MsgID {
				end++
			}
			for _, media := range pendingMedia[start:end] {
				if err := os.Remove(media.LocalPath); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove purged alias media for historical LID %s: %w", raw, err)
				}
			}
			media := pendingMedia[start]
			if err := a.db.ClearMessageLocalMedia(media.ChatJID, media.MsgID); err != nil {
				return fmt.Errorf("clear purged alias media for historical LID %s: %w", raw, err)
			}
			start = end
		}
		if err := a.db.MigrateLIDToPN(raw, pnJID); err != nil {
			return fmt.Errorf("migrate historical LID %s: %w", raw, err)
		}
	}
	return nil
}
