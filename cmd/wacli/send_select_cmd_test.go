package main

import (
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/store"
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
	if err == nil || !strings.Contains(err.Error(), "unsupported control type") {
		t.Fatalf("unsupported error = %v", err)
	}

	_, err = resolveSelectOption([]store.Button{
		{Type: "quick_reply", DisplayText: "Old", ID: "old"},
	}, selectRequest{Label: "Old"})
	if err == nil || !strings.Contains(err.Error(), "sync this message again with a newer wacli") {
		t.Fatalf("old quick reply error = %v", err)
	}
}

func TestBuildSelectResponseMessageListRow(t *testing.T) {
	msg, err := buildSelectResponseMessage(selectOption{
		Type:         "list_row",
		DisplayText:  "Alpha",
		ID:           "alpha",
		Description:  "First item",
		ResponseType: selectResponseList,
	}, store.Message{MsgID: "inbound1", SenderJID: "15551234567@s.whatsapp.net"}, "")
	if err != nil {
		t.Fatalf("build list response: %v", err)
	}
	resp := msg.GetListResponseMessage()
	if resp == nil {
		t.Fatalf("missing ListResponseMessage")
	}
	if resp.GetTitle() != "Alpha" || resp.GetSingleSelectReply().GetSelectedRowID() != "alpha" || resp.GetDescription() != "First item" {
		t.Fatalf("list response = %+v", resp)
	}
	if resp.GetContextInfo().GetStanzaID() != "inbound1" || resp.GetContextInfo().GetParticipant() != "15551234567@s.whatsapp.net" {
		t.Fatalf("context = %+v", resp.GetContextInfo())
	}
}

func TestBuildSelectResponseMessageClassicButton(t *testing.T) {
	msg, err := buildSelectResponseMessage(selectOption{
		Type:         "quick_reply",
		DisplayText:  "Yes",
		ID:           "yes",
		ResponseType: selectResponseButtons,
	}, store.Message{MsgID: "inbound2"}, "")
	if err != nil {
		t.Fatalf("build button response: %v", err)
	}
	resp := msg.GetButtonsResponseMessage()
	if resp == nil {
		t.Fatalf("missing ButtonsResponseMessage")
	}
	if resp.GetSelectedButtonID() != "yes" || resp.GetSelectedDisplayText() != "Yes" {
		t.Fatalf("button response = %+v", resp)
	}
	if resp.GetContextInfo().GetStanzaID() != "inbound2" {
		t.Fatalf("stanza = %q", resp.GetContextInfo().GetStanzaID())
	}
}

func TestBuildSelectResponseMessageTemplateAndNativeFlow(t *testing.T) {
	msg, err := buildSelectResponseMessage(selectOption{
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

	_, err = buildSelectResponseMessage(selectOption{
		Type:         "quick_reply",
		DisplayText:  "Cancel",
		ID:           "cancel",
		ResponseType: selectResponseInteractive,
	}, store.Message{MsgID: "inbound4"}, "")
	if err == nil || !strings.Contains(err.Error(), "native-flow quick replies are not supported") {
		t.Fatalf("native flow error = %v", err)
	}
}
