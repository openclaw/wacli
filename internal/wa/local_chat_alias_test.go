package wa

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestLocalChatAliasReadsTheStoredMapping(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := t.Context()
	pn := types.NewJID("15550002222", types.DefaultUserServer)
	lid := types.NewJID("222222222222222", types.HiddenUserServer)
	if err := c.client.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	device := pn
	device.Device = 3
	for _, tt := range []struct {
		in, want types.JID
	}{
		{pn, lid},
		{lid, pn},
		{device, lid},
		{types.NewJID("15550003333", types.DefaultUserServer), types.EmptyJID},
		{types.NewJID("120363000000000001", types.GroupServer), types.EmptyJID},
	} {
		got, err := c.LocalChatAlias(ctx, tt.in)
		if err != nil || got != tt.want {
			t.Fatalf("LocalChatAlias(%s) = %s, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

// failingLIDStore stands in for a session store whose mapping reads fail.
type failingLIDStore struct {
	waStore.LIDStore
	err error
}

func (s failingLIDStore) GetLIDForPN(context.Context, types.JID) (types.JID, error) {
	return types.EmptyJID, s.err
}

func (s failingLIDStore) GetPNForLID(context.Context, types.JID) (types.JID, error) {
	return types.EmptyJID, s.err
}

func TestLocalChatAliasReportsAFailedRead(t *testing.T) {
	ownPN := types.NewJID("15551234567", types.DefaultUserServer)
	ownLID := types.NewJID("999123456789", types.HiddenUserServer)
	readErr := errors.New("session store unavailable")
	c := &Client{client: &whatsmeow.Client{Store: &waStore.Device{ID: &ownPN, LID: ownLID, LIDs: failingLIDStore{err: readErr}}}}
	ctx := context.Background()
	for _, jid := range []types.JID{
		types.NewJID("15550002222", types.DefaultUserServer),
		types.NewJID("222222222222222", types.HiddenUserServer),
	} {
		if got, err := c.LocalChatAlias(ctx, jid); !errors.Is(err, readErr) || !got.IsEmpty() {
			t.Fatalf("LocalChatAlias(%s) = %s, %v; want the read error", jid, got, err)
		}
	}
	// The account's own identities come from the device, with no lookup.
	if got, err := c.LocalChatAlias(ctx, ownPN); err != nil || got != ownLID {
		t.Fatalf("own phone number = %s, %v; want %s", got, err, ownLID)
	}
	if got, err := c.LocalChatAlias(ctx, ownLID); err != nil || got != ownPN {
		t.Fatalf("own LID = %s, %v; want %s", got, err, ownPN)
	}
	if _, err := (&Client{}).LocalChatAlias(ctx, ownPN); err == nil {
		t.Fatal("client without a session returned a mapping")
	}
}
