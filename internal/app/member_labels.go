package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
)

// recordMessageMemberLabel keeps the label a group message carried for its
// sender. The label was in effect when the message was sent, so the message
// time dates it; a label-change message is dated by the change itself.
func (a *App) recordMessageMemberLabel(ctx context.Context, pm wa.ParsedMessage) {
	if pm.MemberLabel == nil || pm.Chat.Server != types.GroupServer || strings.TrimSpace(pm.SenderJID) == "" {
		return
	}
	at := pm.Timestamp
	if pm.MemberLabelChange && !pm.MemberLabel.SetAt.IsZero() {
		at = pm.MemberLabel.SetAt
	}
	if at.IsZero() {
		at = pm.MemberLabel.SetAt
	}
	groupJID := canonicalJIDString(pm.Chat)
	userJID := a.canonicalStoreJIDString(ctx, pm.SenderJID)
	if userJID == groupJID {
		// History sync names no participant on this account's own messages,
		// so the sender falls back to the group itself.
		if !pm.FromMe {
			return
		}
		if userJID = a.canonicalStoreJIDString(ctx, a.wa.LinkedJID()); userJID == "" {
			return
		}
	}
	a.recordMemberLabel(store.RecordGroupMemberLabelParams{
		GroupJID: groupJID,
		UserJID:  userJID,
		Label:    pm.MemberLabel.Label,
		At:       at,
	})
}

// handleMemberLabelChange records a label-change message, which has no content
// of its own, so sync neither stores, counts nor forwards it as a message.
func (a *App) handleMemberLabelChange(ctx context.Context, pm wa.ParsedMessage) bool {
	if !pm.MemberLabelChange {
		return false
	}
	pm.Chat = a.canonicalStoreJID(ctx, pm.Chat)
	a.recordMessageMemberLabel(ctx, pm)
	return true
}

// recordHistoryMemberLabels keeps the labels of a synced group's roster. A
// roster label without its own timestamp stays undated: the snapshot's age is
// unknown, so it must not override a label seen on a dated message.
func (a *App) recordHistoryMemberLabels(ctx context.Context, chatID string, conv *waHistorySync.Conversation) {
	group, err := types.ParseJID(chatID)
	if err != nil || group.Server != types.GroupServer {
		return
	}
	groupJID := canonicalJIDString(a.canonicalStoreJID(ctx, group))
	for _, participant := range conv.GetParticipant() {
		label := wa.MemberLabelFromProto(participant.GetMemberLabel())
		user := strings.TrimSpace(participant.GetUserJID())
		if label == nil || user == "" {
			continue
		}
		a.recordMemberLabel(store.RecordGroupMemberLabelParams{
			GroupJID: groupJID,
			UserJID:  a.canonicalStoreJIDString(ctx, user),
			Label:    label.Label,
			At:       label.SetAt,
		})
	}
}

func (a *App) recordMemberLabel(p store.RecordGroupMemberLabelParams) {
	if err := a.db.RecordGroupMemberLabel(p); err != nil {
		a.emitWarning(
			"member_label_store_failed",
			fmt.Sprintf("warning: failed to store member label in %s: %v", p.GroupJID, err),
			map[string]any{"group_jid": p.GroupJID, "error": err.Error()},
		)
	}
}
