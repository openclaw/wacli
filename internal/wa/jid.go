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
	var phone strings.Builder
	for i, ch := range s {
		switch {
		case ch == '+' && i == 0:
			continue
		case ch >= '0' && ch <= '9':
			phone.WriteRune(ch)
		case ch == ' ' || ch == '-' || ch == '(' || ch == ')' || ch == '.':
			continue
		default:
			return "", fmt.Errorf("invalid phone number %q: must contain digits, common formatting, or one leading +", s)
		}
	}
	normalized := phone.String()
	if normalized == "" {
		return "", fmt.Errorf("recipient is required")
	}
	if len(normalized) < 7 || len(normalized) > 15 {
		return "", fmt.Errorf("invalid phone number %q: must be 7-15 digits", s)
	}
	return normalized, nil
}
