package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

type groupPersistResolver map[types.JID]types.JID

func (r groupPersistResolver) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	if resolved, ok := r[jid.ToNonAD()]; ok {
		return resolved
	}
	return jid
}

func TestPersistGroupInfoResolvesKnownLIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	group := types.JID{User: "120363000000", Server: types.GroupServer}
	knownLID := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	knownPN := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	unknownLID := types.JID{User: "888123456789", Server: types.HiddenUserServer}
	info := &types.GroupInfo{
		JID:       group,
		OwnerJID:  knownLID,
		GroupName: types.GroupName{Name: "Project"},
		Participants: []types.GroupParticipant{
			{JID: knownLID, IsAdmin: true},
			{JID: unknownLID},
		},
		GroupCreated: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	resolver := groupPersistResolver{knownLID: knownPN}
	if err := persistGroupInfo(context.Background(), db, resolver, info); err != nil {
		t.Fatalf("persistGroupInfo: %v", err)
	}

	groups, err := db.ListGroups("Project", 1)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].OwnerJID != knownPN.String() {
		t.Fatalf("groups = %+v, want owner %q", groups, knownPN.String())
	}

	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()
	rows, err := raw.Query(`SELECT user_jid, role FROM group_participants WHERE group_jid = ?`, group.String())
	if err != nil {
		t.Fatalf("query participants: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var jid, role string
		if err := rows.Scan(&jid, &role); err != nil {
			t.Fatalf("scan participant: %v", err)
		}
		got[jid] = role
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("participants rows: %v", err)
	}
	if got[knownPN.String()] != "admin" || got[unknownLID.String()] != "member" || len(got) != 2 {
		t.Fatalf("participants = %#v", got)
	}
}
