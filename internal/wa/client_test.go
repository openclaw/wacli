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

	j, err = ParseUserOrJID("+1234567890")
	if err != nil {
		t.Fatalf("ParseUserOrJID with + prefix: %v", err)
	}
	if j.Server != types.DefaultUserServer || j.User != "1234567890" {
		t.Fatalf("expected + to be stripped, got %+v", j)
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
