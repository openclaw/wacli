package app

import (
	"context"
	"fmt"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types/events"
)

func (a *App) handleListEvent(ctx context.Context, evt any) error {
	var err error
	switch v := evt.(type) {
	case *events.LabelEdit:
		if v == nil || v.Action == nil || v.LabelID == "" {
			return nil
		}
		act := v.Action
		var listType *int32
		if act.Type != nil {
			t := int32(*act.Type)
			listType = &t
		}
		err = a.db.UpsertList(store.ListUpdate{
			ID: v.LabelID, Name: act.Name, Color: act.Color, PredefinedID: act.PredefinedID,
			Type: listType, OrderIndex: act.OrderIndex, IsActive: act.IsActive,
			IsImmutable: act.IsImmutable, Deleted: act.Deleted,
			MuteEndTimeMS: act.MuteEndTimeMS,
			Timestamp:     v.Timestamp, FromFullSync: v.FromFullSync,
		})
	case *events.LabelAssociationChat:
		if v == nil || v.Action == nil || v.JID.IsEmpty() || v.LabelID == "" || v.Action.Labeled == nil {
			return nil
		}
		chat := a.canonicalStoreJID(ctx, v.JID)
		jid := canonicalJIDString(chat)
		// A label only needs the chat row to exist so the filter can see it.
		// An empty name keeps a stored one, and skipping name resolution keeps
		// network lookups and the JID fallback out of the ordered writer.
		if v.Action.GetLabeled() {
			err = a.db.UpsertChatMetadata(jid, chatKind(chat), "")
		}
		if err == nil {
			err = a.db.SetChatList(jid, v.LabelID, v.Action.GetLabeled(), v.Timestamp, v.FromFullSync)
		}
	}
	if err != nil {
		a.emitWarning("list_store_failed", fmt.Sprintf("warning: failed to store WhatsApp list event: %v", err), nil)
	}
	return err
}
