package app

import (
	"database/sql"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// These tests assert observable database effects through the real event
// registrations, not the presence of a switch case or a new helper symbol.
func TestReview500ContactEchoThroughProductionHandlers(t *testing.T) {
	for _, handler := range []string{"sync-follow", "one-shot-persistence"} {
		for _, action := range []string{"save", "rename-without-first-name", "clear-names"} {
			t.Run(handler+"/"+action, func(t *testing.T) {
				a := newTestApp(t)
				f := newFakeWA()
				a.wa = f
				pn := types.NewJID("15550001111", types.DefaultUserServer)
				lid := types.NewJID("987654321098765", types.HiddenUserServer)
				f.lids[lid] = pn
				if handler == "sync-follow" {
					installBoundaryReplayHandler(t, a, f)
				} else {
					remove, err := a.AddChatStatePersistenceHandler(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(remove)
				}
				contact := &waSyncAction.ContactAction{FullName: proto.String("Grace Hopper"), FirstName: proto.String("Grace")}
				wantFull, wantFirst := "Grace Hopper", "Grace"
				if action != "save" {
					if err := a.db.UpsertContact(pn.String(), pn.User, "Push", "Old Name", "Old", "Biz"); err != nil {
						t.Fatal(err)
					}
					contact = &waSyncAction.ContactAction{FullName: proto.String("Rear Admiral Hopper")}
					wantFull, wantFirst = "Rear Admiral Hopper", ""
					if action == "clear-names" {
						contact = &waSyncAction.ContactAction{}
						wantFull = ""
					}
				}
				f.applyContactBatch(&events.Contact{JID: lid, Action: contact})()
				if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
					t.Fatal(err)
				}
				raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				var full, first sql.NullString
				if err := raw.QueryRow(`SELECT full_name, first_name FROM contacts WHERE jid = ?`, pn.String()).Scan(&full, &first); err != nil {
					t.Fatalf("contact echo must create/update the mapped PN row: %v", err)
				}
				if full.String != wantFull || first.String != wantFirst {
					t.Fatalf("stored names = %q/%q, want %q/%q (matching whatsmeow PutContactName)", full.String, first.String, wantFull, wantFirst)
				}
			})
		}
	}
}
