package main

import (
	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func persistGroupInfo(db *store.DB, info *types.GroupInfo) error {
	if info == nil {
		return nil
	}
	if err := db.UpsertGroup(info.JID.String(), info.GroupName.Name, info.OwnerJID.String(), info.GroupCreated); err != nil {
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
			UserJID:  p.JID.String(),
			Role:     role,
		})
	}
	return db.ReplaceGroupParticipants(info.JID.String(), ps)
}
