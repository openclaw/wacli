package wa

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

func ParseUserOrJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, fmt.Errorf("recipient is required")
	}
	if strings.Contains(s, "@") {
		return types.ParseJID(s)
	}
	phone, err := normalizePhoneRecipient(s)
	if err != nil {
		return types.JID{}, err
	}
	return types.JID{User: phone, Server: types.DefaultUserServer}, nil
}

func IsGroupJID(jid types.JID) bool {
	return jid.Server == types.GroupServer
}

func normalizePhoneRecipient(s string) (string, error) {
	phone := strings.TrimPrefix(s, "+")
	if phone == "" {
		return "", fmt.Errorf("recipient is required")
	}
	if len(phone) < 7 || len(phone) > 15 {
		return "", fmt.Errorf("invalid phone number %q: must be 7-15 digits", s)
	}
	for _, ch := range phone {
		if ch < '0' || ch > '9' {
			return "", fmt.Errorf("invalid phone number %q: must contain digits only", s)
		}
	}
	return phone, nil
}
