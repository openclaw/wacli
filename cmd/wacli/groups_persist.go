package main

import (
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func canonicalCLIJID(jid types.JID) types.JID {
	if jid.Server == types.DefaultUserServer {
		return jid.ToNonAD()
	}
	return jid
}

func persistGroupInfo(db *store.DB, info *types.GroupInfo) error {
	if info == nil {
		return nil
	}
	if err := db.UpsertGroupWithHierarchy(
		info.JID.String(),
		info.GroupName.Name,
		info.OwnerJID.String(),
		info.GroupCreated,
		info.IsParent,
		info.LinkedParentJID.String(),
	); err != nil {
		return err
	}
	var ps []store.GroupParticipant
	for _, p := range info.Participants {
		role := "member"
		if p.IsSuperAdmin {
			role = "superadmin"
		} else if p.IsAdmin {
			role = "admin"
		}
		ps = append(ps, store.GroupParticipant{
			GroupJID: info.JID.String(),
			UserJID:  canonicalCLIJID(p.JID).String(),
			Role:     role,
		})
	}
	return db.ReplaceGroupParticipants(info.JID.String(), ps)
}

func groupKindLabel(isParent bool, linkedParentJID string) string {
	if isParent {
		return "community"
	}
	if linkedParentJID != "" {
		return "subgroup"
	}
	return "group"
}
