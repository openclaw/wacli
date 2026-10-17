package wa

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
)

// BuildSaveContact builds the app state patch that saves a contact name to the
// WhatsApp account, the same mutation the official clients send when a contact
// is saved in WhatsApp. The phone applies it to its contact list and, when
// phonebook is true, also to the device address book. lid, when known, is the
// contact's LID: current clients key contacts by it and show only the number
// for a contact saved without it.
func BuildSaveContact(target, lid types.JID, fullName, firstName string, phonebook bool) (appstate.PatchInfo, error) {
	fullName = strings.TrimSpace(fullName)
	if fullName == "" {
		return appstate.PatchInfo{}, fmt.Errorf("contact name is required")
	}
	if err := ValidateContactName(fullName); err != nil {
		return appstate.PatchInfo{}, err
	}
	if target.Server != types.DefaultUserServer || target.User == "" {
		return appstate.PatchInfo{}, fmt.Errorf("contact must be a phone number JID, got %s", target)
	}
	firstName = strings.TrimSpace(firstName)
	if firstName == "" {
		firstName = strings.Fields(fullName)[0]
	} else if err := ValidateContactName(firstName); err != nil {
		return appstate.PatchInfo{}, fmt.Errorf("first name: %w", err)
	}
	jid := target.ToNonAD().String()
	action := &waSyncAction.ContactAction{
		FullName:                 &fullName,
		FirstName:                &firstName,
		PnJID:                    &jid,
		SaveOnPrimaryAddressbook: &phonebook,
	}
	if lid.Server == types.HiddenUserServer && lid.User != "" {
		lidJID := lid.ToNonAD().String()
		action.LidJID = &lidJID
	}
	return appstate.PatchInfo{
		Type: appstate.WAPatchCriticalUnblockLow,
		Mutations: []appstate.MutationInfo{{
			Index:   []string{appstate.IndexContact, jid},
			Version: 2,
			Value: &waSyncAction.SyncActionValue{
				ContactAction: action,
			},
		}},
	}, nil
}

// ValidateContactName rejects line breaks and other control characters. No
// client lets a user type them into a contact name, and once saved they
// would reach every device's contact list, logs, and table output.
func ValidateContactName(name string) error {
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("contact name must not contain line breaks or control characters")
		}
	}
	return nil
}

// SaveContact sends the contact mutation. beforeApply runs before whatsmeow
// can advance the collection, like the chat-state writes.
func (c *Client) SaveContact(ctx context.Context, target, lid types.JID, fullName, firstName string, phonebook bool, beforeApply func()) ([]any, error) {
	patch, err := BuildSaveContact(target, lid, fullName, firstName, phonebook)
	if err != nil {
		return nil, err
	}
	return c.sendAppStateWithBoundary(ctx, patch, beforeApply)
}
