package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/store"
)

func testStickerDownload(name string, withMedia bool) store.StickerDownloadInfo {
	digest := sha256.Sum256([]byte(name))
	info := store.StickerDownloadInfo{
		FileHash:   base64.StdEncoding.EncodeToString(digest[:]),
		MimeType:   "image/webp",
		FileSHA256: digest[:],
	}
	if withMedia {
		info.DirectPath = "/v/t62/" + name
		info.MediaKey = make([]byte, 32)
		info.FileEncSHA256 = make([]byte, 32)
	}
	return info
}

func TestDownloadStickersVerifiesCacheAndReportsEachFailure(t *testing.T) {
	dir := t.TempDir()
	write := func(info store.StickerDownloadInfo, body string) {
		if err := os.WriteFile(filepath.Join(dir, stickerFilename(info)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cached := testStickerDownload("cached", true)
	write(cached, "cached")
	stale := testStickerDownload("stale", true)
	write(stale, "tampered")
	expired := testStickerDownload("expired", true)
	noMedia := testStickerDownload("no-media", false)
	// A verified file needs no media metadata; only a fetch does.
	cachedNoMedia := testStickerDownload("cached-no-media", false)
	write(cachedNoMedia, "cached-no-media")
	unverifiable := testStickerDownload("unverifiable", true)
	unverifiable.FileHash, unverifiable.FileSHA256 = "not-a-sha256", nil

	var fetched []string
	download := func(_ context.Context, info store.StickerDownloadInfo, target string) (int64, error) {
		fetched = append(fetched, info.FileHash)
		if info.FileHash == expired.FileHash {
			return 0, errors.New("HTTP 410")
		}
		return 7, os.WriteFile(target, []byte("fetched"), 0o600)
	}
	results := downloadStickers(context.Background(), []store.StickerDownloadInfo{cached, stale, expired, noMedia, cachedNoMedia, unverifiable}, dir, download)

	if len(fetched) != 2 || fetched[0] != stale.FileHash || fetched[1] != expired.FileHash {
		t.Fatalf("fetched %v, want only the unverified and expired stickers", fetched)
	}
	want := []stickerDownloadResult{
		{FileHash: cached.FileHash, Path: filepath.Join(dir, stickerFilename(cached)), Bytes: 6, Source: "cache"},
		{FileHash: stale.FileHash, Path: filepath.Join(dir, stickerFilename(stale)), Bytes: 7, Source: "server"},
		{FileHash: expired.FileHash, Error: "HTTP 410"},
		{FileHash: noMedia.FileHash, Error: "sticker has no downloadable media metadata (run `wacli stickers refresh`)"},
		{FileHash: cachedNoMedia.FileHash, Path: filepath.Join(dir, stickerFilename(cachedNoMedia)), Bytes: 15, Source: "cache"},
		{FileHash: unverifiable.FileHash, Error: "file hash is not a base64 SHA-256 digest, so a download could not be verified"},
	}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("result %d = %+v, want %+v", i, results[i], want[i])
		}
	}
}

func TestStickerFilenameUsesDigestAndType(t *testing.T) {
	info := testStickerDownload("cat", true)
	if got, want := stickerFilename(info), hex.EncodeToString(info.FileSHA256)+".webp"; got != want {
		t.Fatalf("filename = %q, want %q", got, want)
	}
	info.IsLottie = true
	if got := stickerFilename(info); !strings.HasSuffix(got, ".was") {
		t.Fatalf("lottie filename = %q, want .was", got)
	}
	unverifiable := store.StickerDownloadInfo{FileHash: "a/b+c=", MimeType: "image/webp"}
	if got := stickerFilename(unverifiable); strings.Contains(got, "/") {
		t.Fatalf("filename %q escapes the output directory", got)
	}
}

func TestSelectStickersByHashRejectsUnknownHash(t *testing.T) {
	stickers := []store.StickerDownloadInfo{testStickerDownload("a", true), testStickerDownload("b", true)}
	got, err := selectStickersByHash(stickers, []string{stickers[1].FileHash})
	if err != nil || len(got) != 1 || got[0].FileHash != stickers[1].FileHash {
		t.Fatalf("selected = %+v, %v", got, err)
	}
	if _, err := selectStickersByHash(stickers, []string{"missing"}); err == nil {
		t.Fatal("unknown hash selected")
	}
}
