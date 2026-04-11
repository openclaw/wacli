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

// TestParseUserOrJIDPlusPrefix verifies that a leading '+' is stripped from
// phone numbers so "+15551234567" and "15551234567" produce the same JID (#28).
func TestParseUserOrJIDPlusPrefix(t *testing.T) {
	cases := []struct {
		input    string
		wantUser string
	}{
		{"+15551234567", "15551234567"},
		{"+49123456789", "49123456789"},
		{"15551234567", "15551234567"},   // no + — unchanged
		{"+1 555 123 4567", "1 555 123 4567"}, // spaces preserved (WA strips them)
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			j, err := ParseUserOrJID(tc.input)
			if err != nil {
				t.Fatalf("ParseUserOrJID(%q): %v", tc.input, err)
			}
			if j.User != tc.wantUser {
				t.Errorf("ParseUserOrJID(%q).User = %q, want %q", tc.input, j.User, tc.wantUser)
			}
		})
	}
}
