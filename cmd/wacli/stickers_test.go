package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
)

func testSticker(name string, downloadable bool) store.Sticker {
	digest := sha256.Sum256([]byte(name))
	s := store.Sticker{
		FileHash:     base64.StdEncoding.EncodeToString(digest[:]),
		Favorite:     true,
		MimeType:     "image/webp",
		Downloadable: downloadable,
	}
	s.Media.FileSHA256 = digest[:]
	if downloadable {
		s.Media.DirectPath = "/v/t62/" + name
		s.Media.MediaKey = make([]byte, 32)
		s.Media.FileEncSHA256 = make([]byte, 32)
	}
	return s
}

func TestDownloadStickersVerifiesCacheAndReportsEachFailure(t *testing.T) {
	dir := t.TempDir()
	cached := testSticker("cached", true)
	if err := os.WriteFile(filepath.Join(dir, stickerFilename(cached)), []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := testSticker("stale", true)
	if err := os.WriteFile(filepath.Join(dir, stickerFilename(stale)), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	expired := testSticker("expired", true)
	noDescriptor := testSticker("no-descriptor", false)
	// A verified file needs no descriptor; only a fetch does.
	cachedNoDescriptor := testSticker("cached-no-descriptor", false)
	if err := os.WriteFile(filepath.Join(dir, stickerFilename(cachedNoDescriptor)), []byte("cached-no-descriptor"), 0o600); err != nil {
		t.Fatal(err)
	}
	unverifiable := testSticker("unverifiable", true)
	unverifiable.FileHash, unverifiable.Media.FileSHA256 = "not-a-sha256", nil

	var fetched []string
	download := func(_ context.Context, s store.Sticker, target string) (int64, error) {
		fetched = append(fetched, s.FileHash)
		if s.FileHash == expired.FileHash {
			return 0, errors.New("HTTP 410")
		}
		return 7, os.WriteFile(target, []byte("fetched"), 0o600)
	}
	results := downloadStickers(context.Background(), []store.Sticker{cached, stale, expired, noDescriptor, cachedNoDescriptor, unverifiable}, dir, download)

	if len(fetched) != 2 || fetched[0] != stale.FileHash || fetched[1] != expired.FileHash {
		t.Fatalf("fetched %v, want only the unverified and expired stickers", fetched)
	}
	want := []stickerDownloadResult{
		{FileHash: cached.FileHash, Path: filepath.Join(dir, stickerFilename(cached)), Bytes: 6, Source: "cache"},
		{FileHash: stale.FileHash, Path: filepath.Join(dir, stickerFilename(stale)), Bytes: 7, Source: "server"},
		{FileHash: expired.FileHash, Error: "HTTP 410"},
		{FileHash: noDescriptor.FileHash, Error: "no download descriptor stored; run `wacli stickers refresh`"},
		{FileHash: cachedNoDescriptor.FileHash, Path: filepath.Join(dir, stickerFilename(cachedNoDescriptor)), Bytes: 20, Source: "cache"},
		{FileHash: unverifiable.FileHash, Error: "file hash is not a base64 SHA-256 digest, so a download could not be verified"},
	}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("result %d = %+v, want %+v", i, results[i], want[i])
		}
	}
}

func TestStickerFilenameUsesDigestAndType(t *testing.T) {
	s := testSticker("cat", true)
	if got, want := stickerFilename(s), hex.EncodeToString(s.Media.FileSHA256)+".webp"; got != want {
		t.Fatalf("filename = %q, want %q", got, want)
	}
	s.IsLottie = true
	if got := stickerFilename(s); !strings.HasSuffix(got, ".was") {
		t.Fatalf("lottie filename = %q, want .was", got)
	}
	unverifiable := store.Sticker{FileHash: "a/b+c=", MimeType: "image/webp"}
	if got := stickerFilename(unverifiable); strings.Contains(got, "/") {
		t.Fatalf("filename %q escapes the output directory", got)
	}
}

func TestSelectStickersByHashRejectsUnknownHash(t *testing.T) {
	stickers := []store.Sticker{testSticker("a", true), testSticker("b", true)}
	got, err := selectStickersByHash(stickers, []string{stickers[1].FileHash})
	if err != nil || len(got) != 1 || got[0].FileHash != stickers[1].FileHash {
		t.Fatalf("selected = %+v, %v", got, err)
	}
	if _, err := selectStickersByHash(stickers, []string{"missing"}); err == nil {
		t.Fatal("unknown hash selected")
	}
}

func TestExplainStickersRefreshDelegateErrorNamesTheRestart(t *testing.T) {
	rejected := fmt.Errorf(`unsupported send kind %q`, stickersRefreshKind)
	if err := explainStickersRefreshDelegateError(rejected); err == nil || !strings.Contains(err.Error(), "restart `wacli sync`") {
		t.Fatalf("error = %v, want the restart hint", err)
	}
	other := errors.New("connection refused")
	if got := explainStickersRefreshDelegateError(other); got != other {
		t.Fatalf("unrelated error = %v, want it unchanged", got)
	}
}

func TestDelegatedStickersRefreshDoesNotWaitForSendQueue(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	sendSlot := make(chan struct{}, 1) // empty means an earlier send owns it
	go handleSendDelegateConn(context.Background(), serverConn, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return sendDelegateResponse{OK: true, Action: req.Kind}, nil
	}, sendSlot, newSendPacer(sendSpacing{}))

	if err := json.NewEncoder(clientConn).Encode(sendDelegateRequest{Version: sendDelegateVersion, Kind: stickersRefreshKind}); err != nil {
		t.Fatal(err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("stickers refresh waited behind the send queue: %v", err)
	}
	if !resp.OK || resp.Action != stickersRefreshKind {
		t.Fatalf("response = %+v", resp)
	}
}

type fakeStickersRefreshApp struct {
	refresh func(context.Context) error
}

func (f fakeStickersRefreshApp) RefreshFavoriteStickers(ctx context.Context) error {
	return f.refresh(ctx)
}

func TestStickersRefreshDelegatesToRunningSyncOwner(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()

	owner := fakeStickersRefreshApp{refresh: func(context.Context) error {
		db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
		if err != nil {
			return err
		}
		defer db.Close()
		return db.SetFavoriteSticker(store.StickerParams{FileHash: testSticker("cat", false).FileHash}, true, time.Now())
	}}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Kind != stickersRefreshKind {
			return sendDelegateResponse{}, fmt.Errorf("unexpected delegated kind %q", req.Kind)
		}
		return executeDelegatedStickersRefresh(ctx, owner)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--json", "--timeout", "5s", "stickers", "refresh"})
	if err != nil {
		t.Fatalf("stickers refresh failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if !strings.Contains(stdout, `"favorites":1`) {
		t.Fatalf("stdout = %q, want one favorite reported", stdout)
	}
}
