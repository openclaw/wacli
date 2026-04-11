package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestParseUserOrJID(t *testing.T) {
	t.Run("plain digits", func(t *testing.T) {
		j, err := ParseUserOrJID("1234567890")
		if err != nil {
			t.Fatalf("ParseUserOrJID: %v", err)
		}
		if j.Server != types.DefaultUserServer || j.User != "1234567890" {
			t.Fatalf("unexpected jid: %+v", j)
		}
	})

	t.Run("plus e164", func(t *testing.T) {
		j, err := ParseUserOrJID("+15551234567")
		if err != nil {
			t.Fatalf("ParseUserOrJID plus: %v", err)
		}
		if j.Server != types.DefaultUserServer || j.User != "15551234567" {
			t.Fatalf("unexpected jid: %+v", j)
		}
	})

	t.Run("formatted phone", func(t *testing.T) {
		j, err := ParseUserOrJID("+1 (555) 123-4567")
		if err != nil {
			t.Fatalf("ParseUserOrJID formatted: %v", err)
		}
		if j.Server != types.DefaultUserServer || j.User != "15551234567" {
			t.Fatalf("unexpected jid: %+v", j)
		}
	})

	t.Run("group jid", func(t *testing.T) {
		j, err := ParseUserOrJID("123@g.us")
		if err != nil {
			t.Fatalf("ParseUserOrJID group: %v", err)
		}
		if !IsGroupJID(j) {
			t.Fatalf("expected group jid, got %+v", j)
		}
	})

	t.Run("invalid phone", func(t *testing.T) {
		if _, err := ParseUserOrJID("+1-800-FLOWERS"); err == nil {
			t.Fatalf("expected invalid phone number error")
		}
	})

	t.Run("plus only allowed first", func(t *testing.T) {
		if _, err := ParseUserOrJID("12+34"); err == nil {
			t.Fatalf("expected invalid phone number error")
		}
	})

	t.Run("unicode digits rejected", func(t *testing.T) {
		if _, err := ParseUserOrJID("١٢٣٤"); err == nil {
			t.Fatalf("expected invalid phone number error")
		}
	})
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
