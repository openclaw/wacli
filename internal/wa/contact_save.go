package wa

import (
	"context"
	"fmt"
	"strings"

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
	if target.Server != types.DefaultUserServer || target.User == "" {
		return appstate.PatchInfo{}, fmt.Errorf("contact must be a phone number JID, got %s", target)
	}
	firstName = strings.TrimSpace(firstName)
	if firstName == "" {
		firstName = strings.Fields(fullName)[0]
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

func (c *Client) SaveContact(ctx context.Context, target, lid types.JID, fullName, firstName string, phonebook bool) error {
	patch, err := BuildSaveContact(target, lid, fullName, firstName, phonebook)
	if err != nil {
		return err
	}
	return c.SendAppState(ctx, patch)
}
