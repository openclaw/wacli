package app

import (
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func TestEnsureAuthedMigratesHistoricalLIDs(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	lid := types.JID{User: "999123456789", Device: 42, Server: types.HiddenUserServer}
	lidNonAD := lid.ToNonAD()
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[lidNonAD] = pn

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(lid.String(), "unknown", lid.String(), base); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}
	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   lid.String(),
		MsgID:     "m-lid",
		SenderJID: lid.String(),
		Timestamp: base,
		Text:      "historical",
	}); err != nil {
		t.Fatalf("UpsertMessage lid: %v", err)
	}

	if err := a.EnsureAuthed(); err != nil {
		t.Fatalf("EnsureAuthed: %v", err)
	}

	msg, err := a.db.GetMessage(pn.String(), "m-lid")
	if err != nil {
		t.Fatalf("GetMessage pn: %v", err)
	}
	if msg.ChatJID != pn.String() {
		t.Fatalf("ChatJID = %q, want %q", msg.ChatJID, pn.String())
	}
	if msg.SenderJID != pn.String() {
		t.Fatalf("SenderJID = %q, want %q", msg.SenderJID, pn.String())
	}
	lids, err := a.db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs: %v", err)
	}
	if len(lids) != 0 {
		t.Fatalf("HistoricalLIDJIDs = %#v, want none", lids)
	}
}

func TestEnsureAuthedLeavesUnresolvedHistoricalLIDs(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f

	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(lid.String(), "unknown", lid.String(), base); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}

	if err := a.EnsureAuthed(); err != nil {
		t.Fatalf("EnsureAuthed: %v", err)
	}
	lids, err := a.db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs: %v", err)
	}
	if len(lids) != 1 || lids[0] != lid.String() {
		t.Fatalf("HistoricalLIDJIDs = %#v, want %q", lids, lid.String())
	}
}
