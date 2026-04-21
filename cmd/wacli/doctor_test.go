package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
)

func TestParseLockOwnerPID(t *testing.T) {
	tests := []struct {
		name string
		info string
		want int
	}{
		{name: "pid line", info: "pid=50394\nacquired_at=2026-04-05T12:30:11Z", want: 50394},
		{name: "trimmed pid", info: " pid= 42 ", want: 42},
		{name: "missing pid", info: "acquired_at=2026-04-05T12:30:11Z"},
		{name: "invalid pid", info: "pid=abc"},
		{name: "zero pid", info: "pid=0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLockOwnerPID(tc.info); got != tc.want {
				t.Fatalf("parseLockOwnerPID() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDoctorConnectionState(t *testing.T) {
	tests := []struct {
		name      string
		authed    bool
		connected bool
		lockHeld  bool
		connect   bool
		want      string
	}{
		{name: "connected wins", authed: true, connected: true, lockHeld: true, want: "connected"},
		{name: "locked paired session", authed: true, lockHeld: true, want: "locked_by_other_process"},
		{name: "connect requested stays disconnected", authed: true, lockHeld: true, connect: true, want: "disconnected"},
		{name: "plain disconnected", authed: true, want: "disconnected"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := doctorConnectionState(tc.authed, tc.connected, tc.lockHeld, tc.connect)
			if got != tc.want {
				t.Fatalf("doctorConnectionState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDoctorStoreStatsFromStoreStats(t *testing.T) {
	when := time.Date(2024, 4, 1, 12, 30, 0, 0, time.FixedZone("offset", 2*60*60))
	got := doctorStoreStatsFromStoreStats(store.StoreStats{
		Messages:      4,
		Chats:         3,
		Contacts:      2,
		Groups:        1,
		LastMessageTS: when.Unix(),
	})
	if got.Messages != 4 || got.Chats != 3 || got.Contacts != 2 || got.Groups != 1 {
		t.Fatalf("unexpected counts: %+v", got)
	}
	if got.LastSyncAt != "2024-04-01T10:30:00Z" {
		t.Fatalf("LastSyncAt = %q", got.LastSyncAt)
	}
}

func TestWriteDoctorReportIncludesLinkedJIDAndStats(t *testing.T) {
	var b bytes.Buffer
	writeDoctorReport(&b, doctorReport{
		StoreDir:        "/tmp/wacli",
		Authed:          true,
		LinkedJID:       "1234567890@s.whatsapp.net",
		ConnectionState: "disconnected",
		FTSEnabled:      true,
		Store: &doctorStoreStats{
			Messages:   9,
			Chats:      8,
			Contacts:   7,
			Groups:     6,
			LastSyncAt: "2024-04-01T10:30:00Z",
		},
	})

	out := b.String()
	for _, want := range []string{
		"LINKED_JID",
		"1234567890@s.whatsapp.net",
		"MESSAGES",
		"9",
		"CHATS",
		"8",
		"CONTACTS",
		"7",
		"GROUPS",
		"6",
		"LAST_SYNC",
		"2024-04-01T10:30:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}
