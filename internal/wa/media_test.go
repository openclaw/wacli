package wa

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/util/cbcutil"
)

func TestMediaTypeFromString(t *testing.T) {
	for _, tc := range []string{"image", "video", "audio", "document", "sticker"} {
		if _, err := MediaTypeFromString(tc); err != nil {
			t.Fatalf("expected %s to be supported: %v", tc, err)
		}
	}
	if _, err := MediaTypeFromString("nope"); err == nil {
		t.Fatalf("expected error for unsupported type")
	}
}

func TestMediaDownloadLengthRejectsOversizedMedia(t *testing.T) {
	_, err := mediaDownloadLength(MaxMediaDownloadSize + 1)
	if err == nil || !strings.Contains(err.Error(), "media too large") {
		t.Fatalf("expected media too large error, got %v", err)
	}
}

func TestMediaDownloadLength(t *testing.T) {
	if got, err := mediaDownloadLength(0); err != nil || got != -1 {
		t.Fatalf("length(0) = %d, %v; want -1, nil", got, err)
	}
	if got, err := mediaDownloadLength(123); err != nil || got != 123 {
		t.Fatalf("length(123) = %d, %v; want 123, nil", got, err)
	}
}

func TestDownloadMediaDirectToFile(t *testing.T) {
	plaintext := []byte("voice note bytes")
	mediaKey := bytes.Repeat([]byte{7}, 32)
	iv, cipherKey, macKey := directMediaKeys(mediaKey, whatsmeow.MediaAudio)
	ciphertext, err := cbcutil.Encrypt(cipherKey, iv, append([]byte(nil), plaintext...))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	mac := hmac.New(sha256.New, macKey)
	mac.Write(iv)
	mac.Write(ciphertext)
	encrypted := append(append([]byte(nil), ciphertext...), mac.Sum(nil)[:mediaHMACLength]...)
	encHash := sha256.Sum256(encrypted)
	fileHash := sha256.Sum256(plaintext)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "" || r.Header.Get("Referer") == "" {
			t.Fatalf("missing WhatsApp media headers")
		}
		if r.URL.Path != "/voice.ogg" {
			t.Fatalf("path = %q, want /voice.ogg", r.URL.Path)
		}
		if got, want := r.URL.Query().Get("hash"), base64.URLEncoding.EncodeToString(encHash[:]); got != want {
			t.Fatalf("hash query = %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("mms-type"); got != "audio" {
			t.Fatalf("mms-type query = %q, want audio", got)
		}
		if !strings.Contains(r.URL.RawQuery, "__wa-mms=") {
			t.Fatalf("raw query %q missing __wa-mms", r.URL.RawQuery)
		}
		_, _ = w.Write(encrypted)
	}))
	defer server.Close()
	oldBaseURL := directMediaBaseURL
	directMediaBaseURL = server.URL
	defer func() {
		directMediaBaseURL = oldBaseURL
	}()

	target := filepath.Join(t.TempDir(), "voice.ogg")
	n, err := DownloadMediaDirectToFile(context.Background(), "/voice.ogg", encHash[:], fileHash[:], mediaKey, uint64(len(plaintext)), "audio", target)
	if err != nil {
		t.Fatalf("DownloadMediaDirectToFile: %v", err)
	}
	if n != int64(len(plaintext)) {
		t.Fatalf("downloaded bytes = %d, want %d", n, len(plaintext))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("output = %q, want %q", got, plaintext)
	}
}

func TestDownloadMediaDirectToFileRejectsMalformedDirectPath(t *testing.T) {
	target := filepath.Join(t.TempDir(), "voice.ogg")
	_, err := DownloadMediaDirectToFile(context.Background(), "not-a-path", nil, nil, bytes.Repeat([]byte{7}, 32), 0, "audio", target)
	if err == nil || !strings.Contains(err.Error(), "does not start with slash") {
		t.Fatalf("DownloadMediaDirectToFile error = %v, want malformed direct path", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target stat err = %v, want not exist", statErr)
	}
}

func TestDownloadMediaDirectToFileRejectsAbsoluteURL(t *testing.T) {
	target := filepath.Join(t.TempDir(), "voice.ogg")
	_, err := DownloadMediaDirectToFile(context.Background(), "https://example.com/voice.ogg", nil, nil, bytes.Repeat([]byte{7}, 32), 0, "audio", target)
	if err == nil || !strings.Contains(err.Error(), "not a URL") {
		t.Fatalf("DownloadMediaDirectToFile error = %v, want absolute URL rejection", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target stat err = %v, want not exist", statErr)
	}
}

func TestLimitedDownloadFileRejectsWritesPastLimit(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "download.bin"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer f.Close()

	limited := &limitedDownloadFile{File: f, max: 5}
	if n, err := limited.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("Write = %d, %v; want 5, nil", n, err)
	}
	if _, err := limited.Write([]byte("!")); err == nil || !strings.Contains(err.Error(), "media too large") {
		t.Fatalf("expected media too large error, got %v", err)
	}
	if _, err := limited.WriteAt([]byte("x"), 5); err == nil || !strings.Contains(err.Error(), "media too large") {
		t.Fatalf("expected WriteAt media too large error, got %v", err)
	}
	if _, err := limited.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if n, err := limited.Write([]byte("hey")); err != nil || n != 3 {
		t.Fatalf("retry Write = %d, %v; want 3, nil", n, err)
	}
	if err := limited.Truncate(2); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if _, err := limited.WriteAt([]byte("!"), 4); err != nil {
		t.Fatalf("WriteAt after truncate: %v", err)
	}
}

func TestLimitedDownloadFileReadFromEnforcesLimit(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "download.bin"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer f.Close()

	limited := &limitedDownloadFile{File: f, max: 5}
	n, err := limited.ReadFrom(bytes.NewReader([]byte("hello!")))
	if err == nil || !strings.Contains(err.Error(), "media too large") {
		t.Fatalf("ReadFrom = %d, %v; want media too large error", n, err)
	}
	if n != 5 {
		t.Fatalf("ReadFrom bytes = %d, want 5", n)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != 5 {
		t.Fatalf("file size = %d, want 5", info.Size())
	}
}

func TestLimitedDownloadFileAllowsEncryptedOverheadBeforeTruncate(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "download.bin"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer f.Close()

	limited := &limitedDownloadFile{File: f, max: 5 + maxEncryptedMediaDownloadOverhead, userMax: 5}
	if n, err := limited.Write(bytes.Repeat([]byte("x"), 5+maxEncryptedMediaDownloadOverhead)); err != nil || n != 5+maxEncryptedMediaDownloadOverhead {
		t.Fatalf("Write encrypted bytes = %d, %v; want overhead accepted", n, err)
	}
	if err := limited.Truncate(5); err != nil {
		t.Fatalf("Truncate to plaintext size: %v", err)
	}
	if _, err := limited.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if _, err := limited.Write(bytes.Repeat([]byte("x"), 5+maxEncryptedMediaDownloadOverhead+1)); err == nil || !strings.Contains(err.Error(), "maximum download size is 5 bytes") {
		t.Fatalf("expected user-facing media limit error, got %v", err)
	}
}
