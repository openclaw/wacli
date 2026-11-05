package wa

import (
	"fmt"
	"slices"
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
)

func extractContactText(m *waProto.Message, pm *ParsedMessage) {
	if contact := m.GetContactMessage(); contact != nil {
		pm.Text = contactDisplayText(contact)
		return
	}
	if contacts := m.GetContactsArrayMessage(); contacts != nil {
		pm.Text = contactsArrayDisplayText(contacts)
	}
}

func contactDisplayText(contact *waProto.ContactMessage) string {
	if contact == nil {
		return ""
	}
	name, phones := contactDetails(contact)
	if name == "" && len(phones) == 0 {
		return ""
	}
	return formatContactLine(name, phones)
}

func contactsArrayDisplayText(contacts *waProto.ContactsArrayMessage) string {
	if contacts == nil {
		return ""
	}
	var lines []string
	for _, contact := range contacts.GetContacts() {
		if line := contactDisplayText(contact); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		if name := strings.TrimSpace(contacts.GetDisplayName()); name != "" {
			return "Contacts: " + name
		}
		return ""
	}
	if len(lines) == 1 {
		return lines[0]
	}
	return "Contacts:\n" + strings.Join(lines, "\n")
}

func contactDetails(contact *waProto.ContactMessage) (string, []string) {
	name := strings.TrimSpace(contact.GetDisplayName())
	var phones []string
	for _, line := range unfoldedVCardLines(contact.GetVcard()) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field := vCardPropertyName(key)
		value = unescapeVCardValue(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		switch field {
		case "FN":
			if name == "" {
				name = value
			}
		case "TEL":
			phones = appendUnique(phones, value)
		}
	}
	return name, phones
}

// vCardPropertyName returns the upper-cased property name without parameters
// or group. iPhone contact cards group related lines, as in
// "item1.TEL;type=CELL:+39 333 1234567", and the group must not hide the TEL.
func vCardPropertyName(key string) string {
	name, _, _ := strings.Cut(key, ";")
	name = strings.TrimSpace(name)
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		name = name[dot+1:]
	}
	return strings.ToUpper(name)
}

func unfoldedVCardLines(vcard string) []string {
	raw := strings.Split(strings.ReplaceAll(vcard, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += strings.TrimLeft(line, " \t")
			continue
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines
}

func unescapeVCardValue(value string) string {
	replacer := strings.NewReplacer(`\\`, `\`, `\,`, `,`, `\;`, `;`, `\n`, "\n", `\N`, "\n")
	return replacer.Replace(value)
}

func formatContactLine(name string, phones []string) string {
	switch {
	case name != "" && len(phones) > 0:
		return fmt.Sprintf("Contact: %s (%s)", name, strings.Join(phones, ", "))
	case name != "":
		return "Contact: " + name
	default:
		return "Contact: " + strings.Join(phones, ", ")
	}
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
