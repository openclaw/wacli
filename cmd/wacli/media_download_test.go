package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
)

func TestMediaDownloadCacheMissOpensWritableStore(t *testing.T) {
	t.Setenv("WACLI_READONLY", "")
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	chat, id := "111@s.whatsapp.net", "uncached"
	if err := db.UpsertChat(chat, "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: id, SenderJID: chat, Timestamp: time.Now(), MediaType: "audio", MimeType: "audio/ogg", DirectPath: "/uncached", MediaKey: make([]byte, 32)}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(filepath.Join(dir, "wacli.db") + suffix); err != nil {
			t.Fatal(err)
		}
	}

	// Keep WAL sidecars alive in this process, without taking the store lock.
	// The command's process must open its first SQLite handle read-only.
	// No session is authenticated, so reaching the auth check proves that the
	// writable store initialized after the cache miss without connecting to WA.
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", dir, "media", "download", "--chat", chat, "--id", id, "--output", filepath.Join(dir, "output.ogg")})
	if err == nil || !strings.Contains(stderr, "not authenticated") {
		t.Fatalf("download error = %v stdout=%s stderr=%s, want authentication check after writable store open", err, stdout, stderr)
	}
}

// A malformed old location fails before any HTTP request, reproducing the
// observed stale-location bug without exposing account data or using WhatsApp.
func TestMediaDownloadVerifiedCacheBypassesExpiredLocationAndStoreLock(t *testing.T) {
	for _, readOnly := range []bool{true, false} {
		t.Run(map[bool]string{true: "read-only", false: "writable"}[readOnly], func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("exact recovered recording")
			hash := sha256.Sum256(data)
			cached := filepath.Join(dir, "recovered.ogg")
			if err := os.WriteFile(cached, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertChat("111@s.whatsapp.net", "dm", "Synthetic", time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: "111@s.whatsapp.net", MsgID: "exact", SenderJID: "111@s.whatsapp.net", Timestamp: time.Now(), MediaType: "audio", MimeType: "audio/ogg", DirectPath: "expired-location", MediaKey: make([]byte, 32), FileSHA256: hash[:], FileLength: uint64(len(data))}); err != nil {
				t.Fatal(err)
			}
			if err := db.MarkMediaDownloaded("111@s.whatsapp.net", "exact", cached, time.Now()); err != nil {
				t.Fatal(err)
			}
			db.Close()
			lk, err := lock.Acquire(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			target := filepath.Join(dir, "output.ogg")
			args := []string{"--store", dir, "--json", "media", "download", "--chat", "111@s.whatsapp.net", "--id", "exact", "--output", target}
			if readOnly {
				args = append([]string{"--read-only"}, args...)
			}
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err != nil {
				t.Fatalf("cached exact download: %v stdout=%s stderr=%s", err, stdout, stderr)
			}
			if !strings.Contains(stdout, `"source":"cache"`) {
				t.Fatalf("cache not identified: %s", stdout)
			}
			got, err := os.ReadFile(target)
			if err != nil || string(got) != string(data) {
				t.Fatalf("output=%q err=%v", got, err)
			}
		})
	}
}

func TestMediaDownloadPhoneRetryRejectsReadOnlyBeforeOpeningStore(t *testing.T) {
	err := execute([]string{"--read-only", "--store", t.TempDir(), "media", "download", "--chat", "111@s.whatsapp.net", "--id", "exact", "--output", filepath.Join(t.TempDir(), "output"), "--retry-on-phone"})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("error=%v, want read-only rejection", err)
	}
}
