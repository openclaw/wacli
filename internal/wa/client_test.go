package wa

import (
	"path/filepath"
	"testing"

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
		{name: "group jid", input: "123@g.us", wantUser: "123", wantServer: types.GroupServer},
		{name: "empty after plus", input: "+", wantErr: true},
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
