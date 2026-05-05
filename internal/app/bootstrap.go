package app

import "context"

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
		_ = a.db.UpsertGroupWithHierarchy(g.JID.String(), g.GroupName.Name, g.OwnerJID.String(), g.GroupCreated, g.IsParent, g.LinkedParentJID.String())
		_ = a.db.UpsertChat(g.JID.String(), "group", g.GroupName.Name, now)
	}
	return a.db.MarkGroupsMissingFrom(joined, now)
}
