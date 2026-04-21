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

func TestPhoneFromLinkedJID(t *testing.T) {
	if got := phoneFromLinkedJID("123@s.whatsapp.net"); got != "123" {
		t.Fatalf("phoneFromLinkedJID = %q", got)
	}
	if got := phoneFromLinkedJID("not-a-jid"); got != "" {
		t.Fatalf("phoneFromLinkedJID invalid = %q", got)
	}
}
