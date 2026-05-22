package main

import (
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func TestSendSelectCommandRegistered(t *testing.T) {
	cmd := newSendCmd(&rootFlags{})
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "select" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("send select command was not registered")
	}
}

func TestValidateSelectRequestRequiresExactlyOneSelector(t *testing.T) {
	tests := []struct {
		name string
		req  selectRequest
		want string
	}{
		{name: "none", req: selectRequest{}, want: "exactly one"},
		{name: "two", req: selectRequest{Label: "Yes", ButtonID: "yes"}, want: "exactly one"},
		{name: "bad index", req: selectRequest{IndexSet: true}, want: "--index must be 1"},
		{name: "bad type", req: selectRequest{Label: "Yes", Type: "url"}, want: "--type must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSelectRequest(tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}

	if err := validateSelectRequest(selectRequest{Label: "Yes", Type: "quick_reply"}); err != nil {
		t.Fatalf("valid request: %v", err)
	}
}

func TestResolveSelectOption(t *testing.T) {
	buttons := []store.Button{
		{Type: "list", DisplayText: "Menu"},
		{Type: "url", DisplayText: "Open", URL: "https://example.com"},
		{Type: "list_row", DisplayText: "Alpha", ID: "alpha"},
		{Type: "quick_reply", DisplayText: "Beta", ID: "beta", ResponseType: selectResponseButtons},
	}

	byLabel, err := resolveSelectOption(buttons, selectRequest{Label: " Alpha "})
	if err != nil {
		t.Fatalf("label select: %v", err)
	}
	if byLabel.Type != "list_row" || byLabel.ID != "alpha" || byLabel.ResponseType != selectResponseList {
		t.Fatalf("label select = %+v", byLabel)
	}

	byID, err := resolveSelectOption(buttons, selectRequest{ButtonID: " beta "})
	if err != nil {
		t.Fatalf("button-id select: %v", err)
	}
	if byID.Type != "quick_reply" || byID.ID != "beta" || byID.ResponseType != selectResponseButtons {
		t.Fatalf("button-id select = %+v", byID)
	}

	byIndex, err := resolveSelectOption(buttons, selectRequest{Index: 2, IndexSet: true})
	if err != nil {
		t.Fatalf("index select: %v", err)
	}
	if byIndex.ID != "beta" {
		t.Fatalf("index select ID = %q, want beta", byIndex.ID)
	}
}

func TestResolveSelectOptionUsesStoredIndexOrder(t *testing.T) {
	buttons := []store.Button{
		{Type: "quick_reply", DisplayText: "Second", ID: "second", ResponseType: selectResponseTemplate, Index: 2},
		{Type: "quick_reply", DisplayText: "First", ID: "first", ResponseType: selectResponseTemplate, Index: 1},
	}

	byIndex, err := resolveSelectOption(buttons, selectRequest{Index: 1, IndexSet: true})
	if err != nil {
		t.Fatalf("index select: %v", err)
	}
	if byIndex.ID != "first" || byIndex.Index != 1 {
		t.Fatalf("index select = %+v, want first stored index", byIndex)
	}
}

func TestResolveSelectOptionIgnoresNonSelectableMatches(t *testing.T) {
	buttons := []store.Button{
		{Type: "url", DisplayText: "Same", URL: "https://example.com"},
		{Type: "quick_reply", DisplayText: "Same", ID: "same", ResponseType: selectResponseButtons},
	}

	byLabel, err := resolveSelectOption(buttons, selectRequest{Label: "Same"})
	if err != nil {
		t.Fatalf("label select: %v", err)
	}
	if byLabel.ID != "same" {
		t.Fatalf("label select = %+v, want quick reply", byLabel)
	}
}

func TestResolveSelectOptionRejectsAmbiguousAndUnsupported(t *testing.T) {
	_, err := resolveSelectOption([]store.Button{
		{Type: "quick_reply", DisplayText: "Same", ID: "a"},
		{Type: "quick_reply", DisplayText: "Same", ID: "b"},
	}, selectRequest{Label: "Same"})
	if err == nil || !strings.Contains(err.Error(), "multiple options match") {
		t.Fatalf("ambiguous error = %v", err)
	}

	_, err = resolveSelectOption([]store.Button{
		{Type: "url", DisplayText: "Open", URL: "https://example.com"},
	}, selectRequest{Label: "Open"})
	if err == nil || !strings.Contains(err.Error(), "no selectable button or list options") {
		t.Fatalf("unsupported error = %v", err)
	}

	_, err = resolveSelectOption([]store.Button{
		{Type: "quick_reply", DisplayText: "Old", ID: "old"},
	}, selectRequest{Label: "Old"})
	if err == nil || !strings.Contains(err.Error(), "sync this message again with a newer wacli") {
		t.Fatalf("old quick reply error = %v", err)
	}
}

func TestBuildSelectResponseMessageListRowSendsQuotedText(t *testing.T) {
	msg, err := buildSelectResponseMessage(types.JID{User: "15557654321", Server: types.DefaultUserServer}, selectOption{
		Type:         "list_row",
		DisplayText:  "Alpha",
		ID:           "alpha",
		Description:  "First item",
		ResponseType: selectResponseList,
	}, store.Message{MsgID: "inbound1", SenderJID: "15551234567@s.whatsapp.net"}, "")
	if err != nil {
		t.Fatalf("build list response: %v", err)
	}
	resp := msg.GetExtendedTextMessage()
	if resp == nil {
		t.Fatalf("missing ExtendedTextMessage")
	}
	if resp.GetText() != "Alpha" {
		t.Fatalf("list response = %+v", resp)
	}
	if resp.GetContextInfo().GetStanzaID() != "inbound1" || resp.GetContextInfo().GetParticipant() != "15551234567@s.whatsapp.net" {
		t.Fatalf("context = %+v", resp.GetContextInfo())
	}
}

func TestBuildSelectResponseMessageClassicButtonSendsQuotedText(t *testing.T) {
	msg, err := buildSelectResponseMessage(types.JID{User: "15557654321", Server: types.DefaultUserServer}, selectOption{
		Type:         "quick_reply",
		DisplayText:  "Yes",
		ID:           "yes",
		ResponseType: selectResponseButtons,
	}, store.Message{MsgID: "inbound2"}, "")
	if err != nil {
		t.Fatalf("build button response: %v", err)
	}
	resp := msg.GetExtendedTextMessage()
	if resp == nil {
		t.Fatalf("missing ExtendedTextMessage")
	}
	if resp.GetText() != "Yes" {
		t.Fatalf("button response = %+v", resp)
	}
	if resp.GetContextInfo().GetStanzaID() != "inbound2" {
		t.Fatalf("stanza = %q", resp.GetContextInfo().GetStanzaID())
	}
}

func TestBuildSelectResponseMessageTemplateAndNativeFlow(t *testing.T) {
	msg, err := buildSelectResponseMessage(types.JID{User: "15557654321", Server: types.DefaultUserServer}, selectOption{
		Type:         "quick_reply",
		DisplayText:  "Book",
		ID:           "book",
		ResponseType: selectResponseTemplate,
		Index:        2,
	}, store.Message{MsgID: "inbound3"}, "")
	if err != nil {
		t.Fatalf("build template response: %v", err)
	}
	tbr := msg.GetTemplateButtonReplyMessage()
	if tbr == nil {
		t.Fatalf("missing TemplateButtonReplyMessage")
	}
	if tbr.GetSelectedID() != "book" || tbr.GetSelectedDisplayText() != "Book" || tbr.GetSelectedIndex() != 1 {
		t.Fatalf("template response = %+v", tbr)
	}

	_, err = buildSelectResponseMessage(types.JID{User: "15557654321", Server: types.DefaultUserServer}, selectOption{
		Type:         "quick_reply",
		DisplayText:  "Cancel",
		ID:           "cancel",
		ResponseType: selectResponseInteractive,
	}, store.Message{MsgID: "inbound4"}, "")
	if err == nil || !strings.Contains(err.Error(), "native-flow quick replies are not supported") {
		t.Fatalf("native flow error = %v", err)
	}
}

func TestBuildSelectResponseMessageRequiresSenderForUnsyncedGroup(t *testing.T) {
	group := types.JID{User: "12345", Server: types.GroupServer}
	_, err := buildSelectResponseMessage(group, selectOption{
		Type:         "quick_reply",
		DisplayText:  "Yes",
		ID:           "yes",
		ResponseType: selectResponseButtons,
	}, store.Message{MsgID: "inbound5"}, "")
	if err == nil || !strings.Contains(err.Error(), "--sender is required") {
		t.Fatalf("missing sender error = %v", err)
	}

	msg, err := buildSelectResponseMessage(group, selectOption{
		Type:         "quick_reply",
		DisplayText:  "Yes",
		ID:           "yes",
		ResponseType: selectResponseButtons,
	}, store.Message{MsgID: "inbound5"}, "15551234567@s.whatsapp.net")
	if err != nil {
		t.Fatalf("group sender override: %v", err)
	}
	if got := msg.GetExtendedTextMessage().GetContextInfo().GetParticipant(); got != "15551234567@s.whatsapp.net" {
		t.Fatalf("participant = %q", got)
	}
}
