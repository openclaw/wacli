package wa

import (
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestParseUserOrJID(t *testing.T) {
	j, err := ParseUserOrJID("1234567890")
	if err != nil {
		t.Fatalf("ParseUserOrJID: %v", err)
	}
	if j.Server != types.DefaultUserServer || j.User != "1234567890" {
		t.Fatalf("unexpected jid: %+v", j)
	}

	j, err = ParseUserOrJID("123@g.us")
	if err != nil {
		t.Fatalf("ParseUserOrJID group: %v", err)
	}
	if !IsGroupJID(j) {
		t.Fatalf("expected group jid, got %+v", j)
	}
}

func TestBestContactName(t *testing.T) {
	if BestContactName(types.ContactInfo{Found: false, FullName: "x"}) != "" {
		t.Fatalf("expected empty for not found")
	}
	if BestContactName(types.ContactInfo{Found: true, FullName: "Full"}) != "Full" {
		t.Fatalf("expected full name")
	}
	if BestContactName(types.ContactInfo{Found: true, FirstName: "First"}) != "First" {
		t.Fatalf("expected first name")
	}
	if BestContactName(types.ContactInfo{Found: true, BusinessName: "Biz"}) != "Biz" {
		t.Fatalf("expected business name")
	}
	if BestContactName(types.ContactInfo{Found: true, PushName: "Push"}) != "Push" {
		t.Fatalf("expected push name")
	}
}

func TestNewChmodsSQLiteArtifactsAfterInit(t *testing.T) {
	orig := chmodFile
	t.Cleanup(func() { chmodFile = orig })

	dir := t.TempDir()
	path := filepath.Join(dir, "session.db")
	calls := make([]string, 0, 4)
	firstCall := true

	chmodFile = func(target string, mode os.FileMode) error {
		if firstCall {
			firstCall = false
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected session db to exist before chmod, got %v", err)
			}
		}
		calls = append(calls, target)
		return nil
	}

	c, err := New(Options{StorePath: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("expected client")
	}

	want := []string{
		path,
		path + "-wal",
		path + "-shm",
		path + "-journal",
	}
	if len(calls) != len(want) {
		t.Fatalf("expected %d chmod calls, got %d: %v", len(want), len(calls), calls)
	}
	for i, target := range want {
		if calls[i] != target {
			t.Fatalf("chmod call %d: expected %q, got %q", i, target, calls[i])
		}
	}
}
