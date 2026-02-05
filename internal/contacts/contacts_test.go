package contacts

import (
	"testing"
)

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"+14157347847", "14157347847"},
		{"+43 664 104 2436", "436641042436"},
		{"+1 (415) 734-7847", "14157347847"},
		{"14157347847", "14157347847"},
		{"00436641042436", "436641042436"}, // international format with 00
		{"+1-415-734-7847", "14157347847"},
		{"", ""},
		{"+", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := NormalizePhone(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizePhone(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSystemContactName(t *testing.T) {
	tests := []struct {
		contact  SystemContact
		expected string
	}{
		{SystemContact{FirstName: "John", LastName: "Doe"}, "John Doe"},
		{SystemContact{FirstName: "John", LastName: ""}, "John"},
		{SystemContact{FirstName: "", LastName: "Doe"}, "Doe"},
		{SystemContact{FirstName: "", LastName: "", FullName: "John Doe"}, "John Doe"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := tt.contact.Name()
			if got != tt.expected {
				t.Errorf("Name() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestBuildPhoneToNameMap(t *testing.T) {
	contacts := []SystemContact{
		{FirstName: "John", LastName: "Doe", FullName: "John Doe", Phones: []string{"+1 (415) 734-7847"}},
		{FirstName: "Jane", LastName: "Smith", FullName: "Jane Smith", Phones: []string{"+43 664 104 2436", "+43 664 999 8888"}},
	}

	m := BuildPhoneToNameMap(contacts)

	if m["14157347847"] != "John Doe" {
		t.Errorf("Expected John Doe for 14157347847, got %q", m["14157347847"])
	}
	if m["436641042436"] != "Jane Smith" {
		t.Errorf("Expected Jane Smith for 436641042436, got %q", m["436641042436"])
	}
	if m["436649998888"] != "Jane Smith" {
		t.Errorf("Expected Jane Smith for 436649998888, got %q", m["436649998888"])
	}
}
