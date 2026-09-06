package app

import (
	"context"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func (a *App) refreshContacts(ctx context.Context) error {
	if err := a.OpenWA(); err != nil {
		return err
	}
	contacts, err := a.wa.GetAllContacts(ctx)
	if err != nil {
		return err
	}
	for jid, info := range contacts {
		jid = canonicalJID(jid)
		_ = a.db.UpsertContact(
			jid.String(),
			jid.User,
			info.PushName,
			info.FullName,
			info.FirstName,
			info.BusinessName,
		)
	}
	return nil
}

func (a *App) refreshGroups(ctx context.Context) error {
	if err := a.OpenWA(); err != nil {
		return err
	}
	groups, err := a.wa.GetJoinedGroups(ctx)
	if err != nil {
		return err
	}
	now := nowUTC()
	joined := map[string]bool{}
	for _, g := range groups {
		if g == nil {
			continue
		}
		joined[g.JID.String()] = true
		_ = a.storeGroupInfo(ctx, g)
		_ = a.db.UpsertChatMetadata(g.JID.String(), "group", g.GroupName.Name)
	}
	return a.db.MarkGroupsMissingFrom(joined, now)
}

func (a *App) storeGroupInfo(ctx context.Context, group *types.GroupInfo) error {
	ownerJID := a.canonicalStoreJID(ctx, group.OwnerJID).String()
	if err := a.db.UpsertGroupWithHierarchy(
		group.JID.String(),
		group.GroupName.Name,
		ownerJID,
		group.GroupCreated,
		group.IsParent,
		group.LinkedParentJID.String(),
	); err != nil {
		return err
	}

	participants := make([]store.GroupParticipant, 0, len(group.Participants))
	for _, participant := range group.Participants {
		role := "member"
		if participant.IsSuperAdmin {
			role = "superadmin"
		} else if participant.IsAdmin {
			role = "admin"
		}
		participants = append(participants, store.GroupParticipant{
			GroupJID: group.JID.String(),
			UserJID:  a.canonicalStoreJID(ctx, participant.JID).String(),
			Role:     role,
		})
	}
	return a.db.ReplaceGroupParticipants(group.JID.String(), participants)
}

func (a *App) refreshNewsletters(ctx context.Context) error {
	if err := a.OpenWA(); err != nil {
		return err
	}
	list, err := a.wa.GetSubscribedNewsletters(ctx)
	if err != nil {
		return err
	}
	now := nowUTC()
	for _, meta := range list {
		if meta == nil {
			continue
		}
		name := wa.NewsletterName(meta)
		if name == "" {
			name = meta.ID.String()
		}
		_ = a.db.UpsertChat(meta.ID.String(), "newsletter", name, now)
	}
	return nil
}
