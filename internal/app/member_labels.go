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
// sender. Ordinary messages repeat the current label, so the store only adds a
// row when it changed; a label-change message is recorded as such.
func (a *App) recordMessageMemberLabel(ctx context.Context, pm wa.ParsedMessage) {
	if pm.MemberLabel == nil || pm.Chat.Server != types.GroupServer || strings.TrimSpace(pm.SenderJID) == "" {
		return
	}
	source := store.MemberLabelSourceMessage
	if pm.MemberLabelChange {
		source = store.MemberLabelSourceChange
	}
	a.recordMemberLabel(store.RecordGroupMemberLabelParams{
		GroupJID: canonicalJIDString(pm.Chat),
		UserJID:  a.canonicalStoreJIDString(ctx, pm.SenderJID),
		Label:    pm.MemberLabel.Label,
		Source:   source,
		SetAt:    pm.MemberLabel.SetAt,
		SeenAt:   pm.Timestamp,
	})
}

// recordHistoryMemberLabels keeps the labels of a synced group's roster.
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
			Source:   store.MemberLabelSourceHistorySync,
			SetAt:    label.SetAt,
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
