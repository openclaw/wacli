package wa

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func TestNewEnablesRetryMessageStore(t *testing.T) {
	c, err := New(Options{StorePath: filepath.Join(t.TempDir(), "session.db")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	if c.client == nil {
		t.Fatal("expected whatsmeow client")
	}
	if !c.client.UseRetryMessageStore {
		t.Fatal("expected retry message store to be enabled")
	}
	if got := c.LinkedJID(); got != "" {
		t.Fatalf("LinkedJID before auth = %q", got)
	}
}

func TestParseUserOrJID(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantUser   string
		wantServer string
		wantErr    bool
	}{
		{name: "phone", input: "1234567890", wantUser: "1234567890", wantServer: types.DefaultUserServer},
		{name: "phone with plus", input: "+1234567890", wantUser: "1234567890", wantServer: types.DefaultUserServer},
		{name: "phone with spaces and plus", input: " +1234567890 ", wantUser: "1234567890", wantServer: types.DefaultUserServer},
		{name: "formatted phone", input: "+1 (234) 567-8900", wantUser: "12345678900", wantServer: types.DefaultUserServer},
		{name: "dotted phone", input: "1.234.567.8900", wantUser: "12345678900", wantServer: types.DefaultUserServer},
		{name: "minimum length phone", input: "1234567", wantUser: "1234567", wantServer: types.DefaultUserServer},
		{name: "maximum length phone", input: "123456789012345", wantUser: "123456789012345", wantServer: types.DefaultUserServer},
		{name: "group jid", input: "123@g.us", wantUser: "123", wantServer: types.GroupServer},
		{name: "empty after plus", input: "+", wantErr: true},
		{name: "too short phone", input: "123456", wantErr: true},
		{name: "too long phone", input: "1234567890123456", wantErr: true},
		{name: "letters in phone", input: "123abc456", wantErr: true},
		{name: "plus inside phone", input: "12+34567", wantErr: true},
		{name: "double leading plus", input: "++1234567", wantErr: true},
		{name: "unicode digits rejected", input: "١٢٣٤٥٦٧", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := ParseUserOrJID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", j)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseUserOrJID: %v", err)
			}
			if j.Server != tt.wantServer || j.User != tt.wantUser {
				t.Fatalf("unexpected jid: %+v", j)
			}
		})
	}
}

func TestQRChannelEventError(t *testing.T) {
	cases := []struct {
		name string
		evt  whatsmeow.QRChannelItem
		want string
	}{
		{name: "timeout", evt: whatsmeow.QRChannelTimeout, want: "QR code timed out"},
		{name: "client outdated", evt: whatsmeow.QRChannelClientOutdated, want: "WhatsApp client outdated"},
		{name: "multidevice disabled", evt: whatsmeow.QRChannelScannedWithoutMultidevice, want: "multi-device is not enabled"},
		{name: "unexpected state", evt: whatsmeow.QRChannelErrUnexpectedEvent, want: "unexpected QR pairing state"},
		{name: "pair error", evt: whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: errors.New("bad code")}, want: "bad code"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := qrChannelEventError(tt.evt)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
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
