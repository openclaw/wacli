package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAuthStatusPayloadIncludesLinkedJID(t *testing.T) {
	got := authStatusPayload(true, "1234567890@s.whatsapp.net")
	if got["authenticated"] != true {
		t.Fatalf("authenticated = %v", got["authenticated"])
	}
	if got["linked_jid"] != "1234567890@s.whatsapp.net" {
		t.Fatalf("linked_jid = %v", got["linked_jid"])
	}
	if got["phone"] != "1234567890" {
		t.Fatalf("phone = %v", got["phone"])
	}
}

func TestAuthStatusPayloadOmitsLinkedJIDWhenUnauthed(t *testing.T) {
	got := authStatusPayload(false, "1234567890@s.whatsapp.net")
	if _, ok := got["linked_jid"]; ok {
		t.Fatalf("linked_jid should be omitted: %+v", got)
	}
	if _, ok := got["phone"]; ok {
		t.Fatalf("phone should be omitted: %+v", got)
	}
}

func TestWriteAuthStatus(t *testing.T) {
	tests := []struct {
		name      string
		authed    bool
		linkedJID string
		want      string
	}{
		{name: "linked", authed: true, linkedJID: "1234567890@s.whatsapp.net", want: "Authenticated as 1234567890@s.whatsapp.net"},
		{name: "authed no jid", authed: true, want: "Authenticated."},
		{name: "not authed", want: "Not authenticated. Run `wacli auth`."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			writeAuthStatus(&b, tc.authed, tc.linkedJID)
			if got := strings.TrimSpace(b.String()); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeAuthQRFormat(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "", want: "terminal"},
		{input: " TERMINAL ", want: "terminal"},
		{input: "text", want: "text"},
		{input: "png", wantErr: true},
	}
	for _, tc := range tests {
		got, err := normalizeAuthQRFormat(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("normalizeAuthQRFormat(%q) expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeAuthQRFormat(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalizeAuthQRFormat(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAuthQRWriterText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	authQRWriter("text", &stdout, &stderr)("2@test-code")
	if got := strings.TrimSpace(stdout.String()); got != "2@test-code" {
		t.Fatalf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestNormalizePairPhone(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "", want: ""},
		{input: "+15551234567", want: "15551234567"},
		{input: "15551234567", want: "15551234567"},
		{input: "123@g.us", wantErr: true},
		{input: "123abc", wantErr: true},
	}
	for _, tc := range tests {
		got, err := normalizePairPhone(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("normalizePairPhone(%q) expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizePairPhone(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("normalizePairPhone(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestAuthPairCodeWriter(t *testing.T) {
	var stderr bytes.Buffer
	writer := authPairCodeWriter("15551234567", &stderr)
	if writer == nil {
		t.Fatal("expected writer")
	}
	writer("ABCD-1234")
	got := stderr.String()
	if !strings.Contains(got, "Pairing code for +15551234567: ABCD-1234") {
		t.Fatalf("stderr = %q", got)
	}
	if authPairCodeWriter("", &stderr) != nil {
		t.Fatal("expected nil writer without phone")
	}
}

func TestAuthCommandExposesQRFormat(t *testing.T) {
	cmd := newAuthCmd(&rootFlags{})
	flag := cmd.Flags().Lookup("qr-format")
	if flag == nil {
		t.Fatal("expected --qr-format flag")
	}
	if flag.DefValue != "terminal" {
		t.Fatalf("qr-format default = %q", flag.DefValue)
	}
	if cmd.Flags().Lookup("phone") == nil {
		t.Fatal("expected --phone flag")
	}
}

func TestPhoneFromLinkedJID(t *testing.T) {
	if got := phoneFromLinkedJID("123@s.whatsapp.net"); got != "123" {
		t.Fatalf("phoneFromLinkedJID = %q", got)
	}
	if got := phoneFromLinkedJID("not-a-jid"); got != "" {
		t.Fatalf("phoneFromLinkedJID invalid = %q", got)
	}
}
