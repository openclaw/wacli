package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestParseUserOrJID(t *testing.T) {
	j, err := ParseUserOrJID("1234567890")
	if err != nil {
		t.Fatalf("ParseUserOrJID: %v", err)
	}
	if j.Server != types.DefaultUserServer || j.User != "1234567890" {
		t.Fatalf("unexpected jid: %+v", j)
	}

	j, err = ParseUserOrJID("123@g.us")
	if err != nil {
		t.Fatalf("ParseUserOrJID group: %v", err)
	}
	if !IsGroupJID(j) {
		t.Fatalf("expected group jid, got %+v", j)
	}
}

func TestBestContactName(t *testing.T) {
	if BestContactName(types.ContactInfo{Found: false, FullName: "x"}) != "" {
		t.Fatalf("expected empty for not found")
	}
	if BestContactName(types.ContactInfo{Found: true, FullName: "Full"}) != "Full" {
		t.Fatalf("expected full name")
	}
	if BestContactName(types.ContactInfo{Found: true, FirstName: "First"}) != "First" {
		t.Fatalf("expected first name")
	}
	if BestContactName(types.ContactInfo{Found: true, BusinessName: "Biz"}) != "Biz" {
		t.Fatalf("expected business name")
	}
	if BestContactName(types.ContactInfo{Found: true, PushName: "Push"}) != "Push" {
		t.Fatalf("expected push name")
	}
}

// TestParseUserOrJIDValidation verifies phone number validation (#55).
func TestParseUserOrJIDValidation(t *testing.T) {
	valid := []struct {
		input    string
		wantUser string
	}{
		{"1234567890", "1234567890"},
		{"+1234567890", "1234567890"},   // leading + stripped
		{"1234567", "1234567"},          // 7 digits min
		{"123456789012345", "123456789012345"}, // 15 digits max
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.input, func(t *testing.T) {
			j, err := ParseUserOrJID(tc.input)
			if err != nil {
				t.Fatalf("ParseUserOrJID(%q) unexpected error: %v", tc.input, err)
			}
			if j.User != tc.wantUser {
				t.Fatalf("ParseUserOrJID(%q).User = %q, want %q", tc.input, j.User, tc.wantUser)
			}
		})
	}

	invalid := []string{
		"123456",           // too short (6 digits)
		"1234567890123456", // too long (16 digits)
		"123abc456",        // non-digit characters
		"+123abc",          // non-digit after +
		"",                 // empty
	}
	for _, input := range invalid {
		t.Run("invalid/"+input, func(t *testing.T) {
			_, err := ParseUserOrJID(input)
			if err == nil {
				t.Fatalf("ParseUserOrJID(%q) expected error, got nil", input)
			}
		})
	}
}
