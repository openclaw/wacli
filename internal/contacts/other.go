//go:build !darwin

package contacts

import "fmt"

// SystemContact represents a contact from the system address book
type SystemContact struct {
	FirstName string   `json:"firstName"`
	LastName  string   `json:"lastName"`
	FullName  string   `json:"fullName"`
	Phones    []string `json:"phones"`
}

// Name returns the best display name for the contact
func (c SystemContact) Name() string {
	return c.FullName
}

// GetSystemContacts is not implemented on non-Darwin platforms
func GetSystemContacts() ([]SystemContact, error) {
	return nil, fmt.Errorf("system contacts import is only supported on macOS")
}

// NormalizePhone strips non-digit characters and normalizes phone numbers
func NormalizePhone(phone string) string {
	return ""
}

// BuildPhoneToNameMap creates a map from normalized phone numbers to contact names
func BuildPhoneToNameMap(contacts []SystemContact) map[string]string {
	return nil
}
