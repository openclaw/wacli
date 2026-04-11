package app

import (
	"context"
	"time"
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

	// Build set of currently joined group JIDs.
	joinedSet := make(map[string]bool, len(groups))
	now := time.Now().UTC()
	for _, g := range groups {
		if g == nil {
			continue
		}
		joinedSet[g.JID.String()] = true
		_ = a.db.UpsertGroup(g.JID.String(), g.GroupName.Name, g.OwnerJID.String(), g.GroupCreated)
		_ = a.db.UpsertChat(g.JID.String(), "group", g.GroupName.Name, now)
	}

	// Mark groups in DB that are no longer joined as left.
	allGroups, err := a.db.ListGroups("", 0, true)
	if err == nil {
		for _, g := range allGroups {
			if !joinedSet[g.JID] {
				_ = a.db.MarkGroupLeft(g.JID)
			}
		}
	}

	return nil
}
