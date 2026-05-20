package wa

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/wacli/internal/fsutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
)

const MaxMediaDownloadSize = 100 * 1024 * 1024

var directMediaBaseURL = "https://mmg.whatsapp.net"

// WhatsApp writes encrypted media as padded ciphertext plus a 10-byte MAC before
// truncating and decrypting it in place.
const maxEncryptedMediaDownloadOverhead = 16 + 10

func MediaTypeFromString(mediaType string) (whatsmeow.MediaType, error) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image":
		return whatsmeow.MediaImage, nil
	case "video":
		return whatsmeow.MediaVideo, nil
	case "audio":
		return whatsmeow.MediaAudio, nil
	case "document":
		return whatsmeow.MediaDocument, nil
	case "sticker":
		return whatsmeow.MediaImage, nil
	default:
		return "", fmt.Errorf("unsupported media type: %s", mediaType)
	}
}

func (c *Client) DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, mmsType string, targetPath string) (int64, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return 0, fmt.Errorf("not connected")
	}
	if strings.TrimSpace(directPath) == "" {
		return 0, fmt.Errorf("direct path is required")
	}
	mt, err := MediaTypeFromString(mediaType)
	if err != nil {
		return 0, err
	}

	if err := fsutil.EnsureWritableDir(filepath.Dir(targetPath)); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".wacli-download-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	success := false
	defer func() {
		_ = tmpFile.Close()
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	length, err := mediaDownloadLength(fileLength)
	if err != nil {
		return 0, err
	}

	limitedFile := &limitedDownloadFile{File: tmpFile, max: MaxMediaDownloadSize + maxEncryptedMediaDownloadOverhead, userMax: MaxMediaDownloadSize}
	if err := cli.DownloadMediaWithPathToFile(ctx, directPath, encFileHash, fileHash, mediaKey, length, mt, mmsType, limitedFile); err != nil {
		return 0, err
	}
	info, err := tmpFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat temp media file: %w", err)
	}
	if info.Size() > MaxMediaDownloadSize {
		return 0, fmt.Errorf("media too large; maximum download size is %d bytes", MaxMediaDownloadSize)
	}
	if err := tmpFile.Sync(); err != nil {
		return 0, fmt.Errorf("flush temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return 0, fmt.Errorf("move media file: %w", err)
	}
	success = true

	info, err = os.Stat(targetPath)
	if err != nil {
		return 0, fmt.Errorf("stat output file: %w", err)
	}
	return info.Size(), nil
}

func DownloadMediaDirectToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType string, targetPath string) (int64, error) {
	if strings.TrimSpace(directPath) == "" {
		return 0, fmt.Errorf("direct path is required")
	}
	mt, err := MediaTypeFromString(mediaType)
	if err != nil {
		return 0, err
	}
	mediaURL, err := directMediaURL(directPath, encFileHash, mt)
	if err != nil {
		return 0, err
	}
	if _, err := mediaDownloadLength(fileLength); err != nil {
		return 0, err
	}

	plaintext, err := downloadAndDecryptDirect(ctx, mediaURL, encFileHash, fileHash, mediaKey, fileLength, mt)
	if err != nil {
		return 0, err
	}
	if len(plaintext) > MaxMediaDownloadSize {
		return 0, fmt.Errorf("media too large; maximum download size is %d bytes", MaxMediaDownloadSize)
	}
	if err := fsutil.EnsureWritableDir(filepath.Dir(targetPath)); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".wacli-download-*")
	if err != nil {
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	success := false
	defer func() {
		_ = tmpFile.Close()
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(plaintext); err != nil {
		return 0, fmt.Errorf("write media file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return 0, fmt.Errorf("flush temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		return 0, fmt.Errorf("move media file: %w", err)
	}
	success = true
	return int64(len(plaintext)), nil
}

func directMediaURL(directPath string, encFileHash []byte, mediaType whatsmeow.MediaType) (string, error) {
	path := strings.TrimSpace(directPath)
	if strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("media download path must be a WhatsApp direct path, not a URL")
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("media download path does not start with slash: %s", path)
	}
	mediaURL := strings.TrimRight(directMediaBaseURL, "/") + path
	if len(encFileHash) > 0 {
		mediaURL = appendDirectMediaQuery(mediaURL, "hash", base64.URLEncoding.EncodeToString(encFileHash))
	}
	if mmsType := directMediaMMSType(mediaType); mmsType != "" {
		mediaURL = appendDirectMediaQuery(mediaURL, "mms-type", mmsType)
	}
	return appendDirectMediaQuery(mediaURL, "__wa-mms", ""), nil
}

func appendDirectMediaQuery(mediaURL, key, value string) string {
	sep := "?"
	if strings.Contains(mediaURL, "?") {
		sep = "&"
	}
	if strings.HasSuffix(mediaURL, "?") || strings.HasSuffix(mediaURL, "&") {
		sep = ""
	}
	return mediaURL + sep + key + "=" + value
}

func directMediaMMSType(mediaType whatsmeow.MediaType) string {
	switch mediaType {
	case whatsmeow.MediaImage:
		return "image"
	case whatsmeow.MediaAudio:
		return "audio"
	case whatsmeow.MediaVideo:
		return "video"
	case whatsmeow.MediaDocument:
		return "document"
	default:
		return ""
	}
}

func downloadAndDecryptDirect(ctx context.Context, mediaURL string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType whatsmeow.MediaType) ([]byte, error) {
	encrypted, err := downloadDirectBytes(ctx, mediaURL)
	if err != nil {
		return nil, err
	}
	if len(encrypted) <= mediaHMACLength {
		return nil, whatsmeow.ErrTooShortFile
	}
	if len(encrypted) > MaxMediaDownloadSize+maxEncryptedMediaDownloadOverhead {
		return nil, fmt.Errorf("media too large; maximum download size is %d bytes", MaxMediaDownloadSize)
	}
	if len(encFileHash) == sha256.Size {
		sum := sha256.Sum256(encrypted)
		if !bytes.Equal(sum[:], encFileHash) {
			return nil, whatsmeow.ErrInvalidMediaEncSHA256
		}
	}

	iv, cipherKey, macKey := directMediaKeys(mediaKey, mediaType)
	ciphertext := encrypted[:len(encrypted)-mediaHMACLength]
	mac := encrypted[len(encrypted)-mediaHMACLength:]
	if err := validateDirectMedia(iv, ciphertext, macKey, mac); err != nil {
		return nil, err
	}
	plaintext, err := cbcutil.Decrypt(cipherKey, iv, append([]byte(nil), ciphertext...))
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt file: %w", err)
	}
	if fileLength > 0 && uint64(len(plaintext)) != fileLength {
		return nil, fmt.Errorf("%w: expected %d, got %d", whatsmeow.ErrFileLengthMismatch, fileLength, len(plaintext))
	}
	if len(fileHash) == sha256.Size {
		sum := sha256.Sum256(plaintext)
		if !bytes.Equal(sum[:], fileHash) {
			return nil, whatsmeow.ErrInvalidMediaSHA256
		}
	}
	return plaintext, nil
}

func downloadDirectBytes(ctx context.Context, mediaURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare media request: %w", err)
	}
	req.Header.Set("Origin", socket.Origin)
	req.Header.Set("Referer", socket.Origin+"/")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, whatsmeow.DownloadHTTPError{Response: resp}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaDownloadSize+maxEncryptedMediaDownloadOverhead+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxMediaDownloadSize+maxEncryptedMediaDownloadOverhead {
		return nil, fmt.Errorf("media too large; maximum download size is %d bytes", MaxMediaDownloadSize)
	}
	return body, nil
}

func directMediaKeys(mediaKey []byte, appInfo whatsmeow.MediaType) (iv, cipherKey, macKey []byte) {
	expanded := hkdfutil.SHA256(mediaKey, nil, []byte(appInfo), 112)
	return expanded[:16], expanded[16:48], expanded[48:80]
}

func validateDirectMedia(iv, ciphertext, macKey, mac []byte) error {
	h := hmac.New(sha256.New, macKey)
	h.Write(iv)
	h.Write(ciphertext)
	if !hmac.Equal(h.Sum(nil)[:mediaHMACLength], mac) {
		return whatsmeow.ErrInvalidMediaHMAC
	}
	return nil
}

const mediaHMACLength = 10

type limitedDownloadFile struct {
	*os.File
	max     int64
	userMax int64
	written int64
}

func (f *limitedDownloadFile) Write(p []byte) (int, error) {
	off, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if off+int64(len(p)) > f.max {
		return 0, f.limitError()
	}
	n, err := f.File.Write(p)
	f.noteWritten(off + int64(n))
	return n, err
}

func (f *limitedDownloadFile) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.max {
		return 0, f.limitError()
	}
	n, err := f.File.WriteAt(p, off)
	f.noteWritten(off + int64(n))
	return n, err
}

func (f *limitedDownloadFile) ReadFrom(r io.Reader) (int64, error) {
	off, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	remaining := f.max - off
	if remaining < 0 {
		remaining = 0
	}
	n, err := io.Copy(f.File, io.LimitReader(r, remaining))
	f.noteWritten(off + n)
	if err != nil {
		return n, err
	}
	var probe [1]byte
	extra, err := r.Read(probe[:])
	if extra > 0 {
		return n, f.limitError()
	}
	if err != nil && err != io.EOF {
		return n, err
	}
	return n, nil
}

func (f *limitedDownloadFile) Truncate(size int64) error {
	if size > f.max {
		return f.limitError()
	}
	if err := f.File.Truncate(size); err != nil {
		return err
	}
	if f.written > size {
		f.written = size
	}
	return nil
}

func (f *limitedDownloadFile) limitError() error {
	max := f.userMax
	if max <= 0 {
		max = f.max
	}
	return fmt.Errorf("media too large; maximum download size is %d bytes", max)
}

func (f *limitedDownloadFile) noteWritten(end int64) {
	if end > f.written {
		f.written = end
	}
}

func mediaDownloadLength(fileLength uint64) (int, error) {
	if fileLength > MaxMediaDownloadSize {
		return 0, fmt.Errorf("media too large (%d bytes); maximum download size is %d bytes", fileLength, MaxMediaDownloadSize)
	}
	if fileLength > 0 {
		return int(fileLength), nil
	}
	return -1, nil
}
