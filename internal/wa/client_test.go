package wa

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
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
	if _, ok := c.client.Log.(*whatsmeowLogger); !ok {
		t.Fatalf("client logger = %T, want *whatsmeowLogger", c.client.Log)
	}
	if got := c.LinkedJID(); got != "" {
		t.Fatalf("LinkedJID before auth = %q", got)
	}
}

func TestBuildDeleteForMePatch(t *testing.T) {
	chat := types.NewJID("123", types.DefaultUserServer)
	sender := types.NewJID("456", types.DefaultUserServer)
	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	patch := buildDeleteForMePatch(types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: false,
		},
		ID:        types.MessageID("mid"),
		Timestamp: ts,
	}, true)

	if patch.Type != appstate.WAPatchRegularHigh {
		t.Fatalf("patch type = %q", patch.Type)
	}
	if len(patch.Mutations) != 1 {
		t.Fatalf("mutations = %d", len(patch.Mutations))
	}
	mut := patch.Mutations[0]
	wantIndex := []string{appstate.IndexDeleteMessageForMe, chat.String(), "mid", "0", sender.String()}
	for i, want := range wantIndex {
		if mut.Index[i] != want {
			t.Fatalf("index[%d] = %q, want %q", i, mut.Index[i], want)
		}
	}
	action := mut.Value.GetDeleteMessageForMeAction()
	if action == nil || !action.GetDeleteMedia() || action.GetMessageTimestamp() != ts.UnixMilli() {
		t.Fatalf("delete-for-me action = %+v", action)
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
		{name: "newsletter jid", input: "123@newsletter", wantUser: "123", wantServer: types.NewsletterServer},
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

func TestNewsletterName(t *testing.T) {
	meta := &types.NewsletterMetadata{
		ThreadMeta: types.NewsletterThreadMetadata{
			Name: types.NewsletterText{Text: "  Launch Notes  "},
		},
	}
	if got := NewsletterName(meta); got != "Launch Notes" {
		t.Fatalf("NewsletterName = %q", got)
	}
	if got := NewsletterName(nil); got != "" {
		t.Fatalf("NewsletterName(nil) = %q", got)
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
