package app

import (
	"context"
	"fmt"
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
		if err := a.db.MigrateLIDToPN(raw, canonicalJIDString(pn)); err != nil {
			return fmt.Errorf("migrate historical LID %s: %w", raw, err)
		}
	}
	return nil
}
