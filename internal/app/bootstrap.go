package app

import (
	"context"
	"time"

	"github.com/steipete/wacli/internal/store"
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
	return refreshGroups(ctx, a.wa, a.db)
}

type groupsStore interface {
	UpsertGroup(jid, name, ownerJID string, created time.Time) error
	UpsertChat(jid, kind, name string, lastTS time.Time) error
	ListGroups(query string, limit int, includeLeft bool) ([]store.Group, error)
	MarkGroupLeft(jid string) error
}

func refreshGroups(ctx context.Context, wa WAClient, db groupsStore) error {
	groups, err := wa.GetJoinedGroups(ctx)
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
		_ = db.UpsertGroup(g.JID.String(), g.GroupName.Name, g.OwnerJID.String(), g.GroupCreated)
		_ = db.UpsertChat(g.JID.String(), "group", g.GroupName.Name, now)
	}

	// Mark groups in DB that are no longer joined as left.
	allGroups, err := db.ListGroups("", 0, true)
	if err != nil {
		return err
	}
	for _, g := range allGroups {
		if !joinedSet[g.JID] {
			if err := db.MarkGroupLeft(g.JID); err != nil {
				return err
			}
		}
	}

	return nil
}
