package main

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

type leaveGroupWAStub struct {
	err    error
	called bool
}

func (f *leaveGroupWAStub) LeaveGroup(ctx context.Context, group types.JID) error {
	f.called = true
	return f.err
}

type leaveGroupDBStub struct {
	err    error
	called bool
	jid    string
}

func (f *leaveGroupDBStub) MarkGroupLeft(jid string) error {
	f.called = true
	f.jid = jid
	return f.err
}

func TestLeaveGroupReturnsMarkGroupLeftError(t *testing.T) {
	wa := &leaveGroupWAStub{}
	db := &leaveGroupDBStub{err: errors.New("mark failed")}
	jid, err := types.ParseJID("12345@g.us")
	if err != nil {
		t.Fatalf("ParseJID: %v", err)
	}

	got := leaveGroup(context.Background(), wa, db, jid)
	if !errors.Is(got, db.err) {
		t.Fatalf("expected mark error, got %v", got)
	}
	if !wa.called {
		t.Fatalf("expected LeaveGroup to be called")
	}
	if !db.called {
		t.Fatalf("expected MarkGroupLeft to be called")
	}
	if db.jid != jid.String() {
		t.Fatalf("expected jid %q, got %q", jid.String(), db.jid)
	}
}
