package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
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

func TestEnsureAuthedRemovesPurgedAliasMediaBeforeLIDMigration(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[lid.ToNonAD()] = pn
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []string{lid.String(), pn.String()} {
		if err := a.db.UpsertChat(chat, "dm", "Alice", base); err != nil {
			t.Fatal(err)
		}
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "mid", Timestamp: base, Text: "payload"}); err != nil {
			t.Fatal(err)
		}
	}
	aliasPath := filepath.Join(t.TempDir(), "alias-media.bin")
	if err := os.WriteFile(aliasPath, []byte("alias media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(pn.String(), "mid", aliasPath, base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMessageRevoked(lid.String(), "mid"); err != nil {
		t.Fatal(err)
	}
	if err := a.db.PurgeMessage(lid.String(), "mid"); err != nil {
		t.Fatal(err)
	}

	if err := a.EnsureAuthed(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(aliasPath); !os.IsNotExist(err) {
		t.Fatalf("purged alias media still exists: %v", err)
	}
	msg, err := a.db.GetMessage(pn.String(), "mid")
	if err != nil {
		t.Fatal(err)
	}
	if msg.PayloadPurgedAt == nil || msg.LocalPath != "" || msg.Text != "" {
		t.Fatalf("migrated purge state = %+v", msg)
	}
}

func TestEnsureAuthedPreservesDuplicateAliasMediaForLaterPurge(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[lid.ToNonAD()] = pn
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []string{lid.String(), pn.String()} {
		if err := a.db.UpsertChat(chat, "dm", "Alice", base); err != nil {
			t.Fatal(err)
		}
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "mid", Timestamp: base, Text: "payload"}); err != nil {
			t.Fatal(err)
		}
	}
	mediaDir := t.TempDir()
	lidPath := filepath.Join(mediaDir, "lid-media.bin")
	pnPath := filepath.Join(mediaDir, "pn-media.bin")
	for path, body := range map[string]string{lidPath: "lid media", pnPath: "pn media"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.db.MarkMediaDownloaded(lid.String(), "mid", lidPath, base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(pn.String(), "mid", pnPath, base); err != nil {
		t.Fatal(err)
	}

	if err := a.EnsureAuthed(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lidPath); err != nil {
		t.Fatalf("alias media missing: %v", err)
	}
	if _, err := os.Stat(pnPath); err != nil {
		t.Fatalf("surviving destination media missing: %v", err)
	}
	msg, err := a.db.GetMessage(pn.String(), "mid")
	if err != nil {
		t.Fatal(err)
	}
	if msg.LocalPath != pnPath {
		t.Fatalf("migrated local path = %q, want %q", msg.LocalPath, pnPath)
	}
	paths, err := a.db.MessageLocalMediaPaths(pn.String(), "mid")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("migrated media paths = %#v, want both copies", paths)
	}
}

func TestEnsureAuthedPreservesAliasMediaWhenDestinationPathIsStale(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[lid.ToNonAD()] = pn
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []string{lid.String(), pn.String()} {
		if err := a.db.UpsertChat(chat, "dm", "Alice", base); err != nil {
			t.Fatal(err)
		}
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "mid", Timestamp: base, Text: "payload"}); err != nil {
			t.Fatal(err)
		}
	}
	mediaDir := t.TempDir()
	lidPath := filepath.Join(mediaDir, "lid-media.bin")
	stalePNPath := filepath.Join(mediaDir, "missing-pn-media.bin")
	if err := os.WriteFile(lidPath, []byte("lid media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(lid.String(), "mid", lidPath, base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(pn.String(), "mid", stalePNPath, base); err != nil {
		t.Fatal(err)
	}

	if err := a.EnsureAuthed(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lidPath); err != nil {
		t.Fatalf("source alias media missing: %v", err)
	}
	paths, err := a.db.MessageLocalMediaPaths(pn.String(), "mid")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("migrated media paths = %#v, want retained alias and destination metadata", paths)
	}
}

func TestEnsureAuthedKeepsMediaWhenDuplicatePathsIdentifySameFile(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	f.lids[lid.ToNonAD()] = pn
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, chat := range []string{lid.String(), pn.String()} {
		if err := a.db.UpsertChat(chat, "dm", "Alice", base); err != nil {
			t.Fatal(err)
		}
		if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "mid", Timestamp: base, Text: "payload"}); err != nil {
			t.Fatal(err)
		}
	}
	mediaDir := t.TempDir()
	mediaPath := filepath.Join(mediaDir, "media.bin")
	aliasPath := filepath.Join(mediaDir, "nested", "..", "media.bin")
	if err := os.Mkdir(filepath.Join(mediaDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(lid.String(), "mid", aliasPath, base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkMediaDownloaded(pn.String(), "mid", mediaPath, base); err != nil {
		t.Fatal(err)
	}

	if err := a.EnsureAuthed(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mediaPath); err != nil {
		t.Fatalf("shared media missing: %v", err)
	}
	msg, err := a.db.GetMessage(pn.String(), "mid")
	if err != nil {
		t.Fatal(err)
	}
	if msg.LocalPath != mediaPath {
		t.Fatalf("migrated local path = %q, want %q", msg.LocalPath, mediaPath)
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

func TestEnsureAuthedSkipsHistoricalLIDMigrationReadOnly(t *testing.T) {
	storeDir := t.TempDir()
	writer, err := New(Options{StoreDir: storeDir})
	if err != nil {
		t.Fatalf("New writer: %v", err)
	}

	lid := types.JID{User: "999123456789", Device: 42, Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := writer.db.UpsertChat(lid.String(), "unknown", lid.String(), base); err != nil {
		t.Fatalf("UpsertChat lid: %v", err)
	}
	writer.Close()

	reader, err := New(Options{StoreDir: storeDir, ReadOnly: true})
	if err != nil {
		t.Fatalf("New read-only: %v", err)
	}
	defer reader.Close()
	f := newFakeWA()
	f.lids[lid.ToNonAD()] = pn
	reader.wa = f

	if err := reader.EnsureAuthed(); err != nil {
		t.Fatalf("EnsureAuthed read-only: %v", err)
	}
	lids, err := reader.db.HistoricalLIDJIDs()
	if err != nil {
		t.Fatalf("HistoricalLIDJIDs: %v", err)
	}
	if len(lids) != 1 || lids[0] != lid.String() {
		t.Fatalf("HistoricalLIDJIDs = %#v, want %q", lids, lid.String())
	}
}
