package main

import (
	"context"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

type fakeChatResolver struct {
	lidToPN map[types.JID]types.JID
	names   map[types.JID]string
}

func (f fakeChatResolver) ResolveChatName(ctx context.Context, chat types.JID, pushName string) string {
	if name, ok := f.names[chat.ToNonAD()]; ok {
		return name
	}
	return chat.String()
}

func (f fakeChatResolver) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	if pn, ok := f.lidToPN[jid.ToNonAD()]; ok {
		pn.Device = jid.Device
		return pn
	}
	return jid
}

func (f fakeChatResolver) ResolvePNToLID(ctx context.Context, jid types.JID) types.JID {
	for lid, pn := range f.lidToPN {
		if pn == jid.ToNonAD() {
			lid.Device = jid.Device
			return lid
		}
	}
	return jid
}

func TestResolveStoredChatsMapsLIDRows(t *testing.T) {
	lid := mustParseJID(t, "999123456789@lid")
	pn := mustParseJID(t, "15551234567@s.whatsapp.net")
	resolver := fakeChatResolver{
		lidToPN: map[types.JID]types.JID{lid: pn},
		names:   map[types.JID]string{pn: "Alice"},
	}

	got := resolveStoredChatsWith(context.Background(), resolver, []store.Chat{{
		JID:           lid.String(),
		Kind:          "unknown",
		Name:          lid.String(),
		LastMessageTS: time.Unix(10, 0),
	}})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(got), got)
	}
	if got[0].JID != pn.String() || got[0].Kind != "dm" || got[0].Name != "Alice" {
		t.Fatalf("resolved chat = %+v", got[0])
	}
}

func TestResolveStoredChatsMergesMappedDuplicates(t *testing.T) {
	lid := mustParseJID(t, "999123456789@lid")
	pn := mustParseJID(t, "15551234567@s.whatsapp.net")
	resolver := fakeChatResolver{
		lidToPN: map[types.JID]types.JID{lid: pn},
		names:   map[types.JID]string{pn: "Alice"},
	}
	old := time.Unix(10, 0)
	newer := time.Unix(20, 0)

	got := resolveStoredChatsWith(context.Background(), resolver, []store.Chat{
		{JID: lid.String(), Kind: "unknown", Name: lid.String(), LastMessageTS: newer},
		{JID: pn.String(), Kind: "dm", Name: "", LastMessageTS: old},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(got), got)
	}
	if got[0].JID != pn.String() || got[0].Name != "Alice" || !got[0].LastMessageTS.Equal(newer) {
		t.Fatalf("merged chat = %+v", got[0])
	}
}
