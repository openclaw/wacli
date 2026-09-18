package main

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

type contactDisplay struct {
	contact store.Contact
	sources []string
	primary bool
}

func searchContactsForDisplay(ctx context.Context, a *app.App, query string, limit int) ([]store.Contact, error) {
	// Match the original rows as well as the canonical identity. The store search
	// includes names hidden by an alias and metadata on either half of a PN/LID pair.
	matches, err := a.DB().SearchContacts(query, math.MaxInt)
	if err != nil {
		return nil, err
	}
	matched := make(map[string]bool, len(matches))
	for _, contact := range matches {
		matched[contact.JID] = true
	}
	contacts, err := contactsForDisplay(ctx, a)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	needle := strings.ToLower(query)
	queryJID := resolveContactReadJID(ctx, a, query)
	var result []store.Contact
	for _, display := range contacts {
		contact := display.contact
		match := contact.JID == queryJID || strings.Contains(strings.ToLower(contact.JID), needle) || strings.Contains(strings.ToLower(contact.Phone), needle)
		for _, source := range display.sources {
			match = match || matched[source]
		}
		if match {
			result = append(result, contact)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func getContactForDisplay(ctx context.Context, a *app.App, rawJID string) (store.Contact, error) {
	contacts, err := contactsForDisplay(ctx, a)
	if err != nil {
		return store.Contact{}, err
	}
	jid := resolveContactReadJID(ctx, a, rawJID)
	for _, display := range contacts {
		match := display.contact.JID == jid
		for _, source := range display.sources {
			match = match || source == jid
		}
		if !match {
			continue
		}
		contact := display.contact
		tags := make(map[string]bool)
		for _, source := range display.sources {
			values, err := a.DB().ListTags(source)
			if err != nil {
				return store.Contact{}, err
			}
			for _, tag := range values {
				tags[tag] = true
			}
		}
		for tag := range tags {
			contact.Tags = append(contact.Tags, tag)
		}
		sort.Strings(contact.Tags)
		return contact, nil
	}
	return store.Contact{}, sql.ErrNoRows
}

func contactReadResolver(a *app.App) app.LocalResolver {
	if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err != nil {
		return nil
	}
	resolver, _ := a.LocalResolver()
	return resolver
}

func resolveContactReadJID(ctx context.Context, a *app.App, rawJID string) string {
	rawJID = strings.TrimSpace(rawJID)
	jid, err := types.ParseJID(rawJID)
	if err != nil {
		return rawJID
	}
	if jid.Server == types.HiddenUserServer {
		if resolver := contactReadResolver(a); resolver != nil {
			pn := resolver.ResolveLIDToPN(ctx, jid)
			if pn.User != "" && pn.Server == types.DefaultUserServer {
				return pn.ToNonAD().String()
			}
		}
	}
	return canonicalCLIJID(jid).String()
}

func contactsForDisplay(ctx context.Context, a *app.App) ([]contactDisplay, error) {
	// Load before limiting so duplicate rows cannot displace distinct contacts,
	// and a LID-only row is searchable by its resolved phone number.
	contacts, err := a.DB().ListContacts(math.MaxInt)
	if err != nil {
		return nil, err
	}
	var resolver app.LocalResolver
	if len(contacts) > 0 {
		resolver = contactReadResolver(a)
	}
	result := make([]contactDisplay, 0, len(contacts))
	seen := make(map[string]int, len(contacts))
	for _, contact := range contacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source := contact.JID
		jid, err := types.ParseJID(source)
		primary := err == nil && jid.Server == types.DefaultUserServer
		if err == nil && jid.Server == types.HiddenUserServer {
			// A LID's user component is an opaque identifier, never a phone number.
			contact.Phone = ""
			if resolver != nil {
				pn := resolver.ResolveLIDToPN(ctx, jid)
				if pn.User != "" && pn.Server == types.DefaultUserServer {
					contact.JID = pn.ToNonAD().String()
					contact.Phone = pn.User
				}
			}
		} else if primary {
			contact.JID = jid.ToNonAD().String()
			contact.Phone = jid.User
		}
		if idx, ok := seen[contact.JID]; ok {
			display := &result[idx]
			if primary && !display.primary {
				display.contact = mergeDisplayContacts(contact, display.contact)
			} else {
				display.contact = mergeDisplayContacts(display.contact, contact)
			}
			display.primary = display.primary || primary
			display.sources = append(display.sources, source)
			continue
		}
		seen[contact.JID] = len(result)
		result = append(result, contactDisplay{contact: contact, sources: []string{source}, primary: primary})
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i].contact, result[j].contact
		nameA, nameB := a.Name, b.Name
		if nameA == "" {
			nameA = a.JID
		}
		if nameB == "" {
			nameB = b.JID
		}
		if nameA != nameB {
			return nameA < nameB
		}
		return a.JID < b.JID
	})
	return result, nil
}

// Prefer the PN row on conflicts, while retaining local metadata from either
// representation. This is a view only: aliases, tags and rows stay untouched.
func mergeDisplayContacts(primary, secondary store.Contact) store.Contact {
	if primary.Alias == "" {
		primary.Alias = secondary.Alias
	}
	if primary.SystemName == "" {
		primary.SystemName = secondary.SystemName
	}
	switch {
	case primary.Alias != "":
		primary.Name = primary.Alias
	case primary.SystemName != "":
		primary.Name = primary.SystemName
	case primary.Name == "":
		primary.Name = secondary.Name
	}
	if secondary.UpdatedAt.After(primary.UpdatedAt) {
		primary.UpdatedAt = secondary.UpdatedAt
	}
	return primary
}
