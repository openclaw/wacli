package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestResolveChatNameUsesLocalContactForBroadcastList(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	list := types.NewJID("1700000000", types.BroadcastServer)
	if err := c.client.Store.Contacts.PutContactName(t.Context(), list, "", "Choir"); err != nil {
		t.Fatal(err)
	}
	if got := c.ResolveChatName(t.Context(), list, ""); got != "Choir" {
		t.Fatalf("ResolveChatName = %q, want Choir", got)
	}
}
