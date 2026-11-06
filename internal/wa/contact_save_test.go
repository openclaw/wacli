package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

func TestBuildSaveContact(t *testing.T) {
	target := types.JID{User: "5511999990000", Server: types.DefaultUserServer, Device: 3}
	patch, err := BuildSaveContact(target, types.JID{User: "99887766", Server: types.HiddenUserServer}, "  Maria Souza  ", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if patch.Type != appstate.WAPatchCriticalUnblockLow || len(patch.Mutations) != 1 {
		t.Fatalf("patch = %+v", patch)
	}
	m := patch.Mutations[0]
	if len(m.Index) != 2 || m.Index[0] != appstate.IndexContact || m.Index[1] != "5511999990000@s.whatsapp.net" {
		t.Fatalf("index = %v", m.Index)
	}
	if m.Version != 2 {
		t.Fatalf("version = %d", m.Version)
	}
	act := m.Value.GetContactAction()
	if act.GetPnJID() != "5511999990000@s.whatsapp.net" || act.GetLidJID() != "99887766@lid" {
		t.Fatalf("jids = %q %q", act.GetPnJID(), act.GetLidJID())
	}
	if act.GetFullName() != "Maria Souza" || act.GetFirstName() != "Maria" || !act.GetSaveOnPrimaryAddressbook() {
		t.Fatalf("action = %+v", act)
	}
}

func TestBuildSaveContactRejectsBadInput(t *testing.T) {
	pn := types.JID{User: "5511999990000", Server: types.DefaultUserServer}
	if _, err := BuildSaveContact(pn, types.JID{}, "   ", "", false); err == nil {
		t.Fatal("empty name accepted")
	}
	lid := types.JID{User: "123456789", Server: types.HiddenUserServer}
	if _, err := BuildSaveContact(lid, types.JID{}, "Maria", "", false); err == nil {
		t.Fatal("LID accepted")
	}
	group := types.JID{User: "1203630000", Server: types.GroupServer}
	if _, err := BuildSaveContact(group, types.JID{}, "Maria", "", false); err == nil {
		t.Fatal("group accepted")
	}
}

func TestBuildSaveContactKeepsExplicitFirstName(t *testing.T) {
	pn := types.JID{User: "5511999990000", Server: types.DefaultUserServer}
	patch, err := BuildSaveContact(pn, types.JID{}, "Maria Souza", "Mari", false)
	if err != nil {
		t.Fatal(err)
	}
	act := patch.Mutations[0].Value.GetContactAction()
	if act.GetFirstName() != "Mari" || act.GetSaveOnPrimaryAddressbook() || act.LidJID != nil {
		t.Fatalf("action = %+v", act)
	}
}
