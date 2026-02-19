package wa

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.mau.fi/whatsmeow"
)

// MaxMediaDownloadSize is the maximum size for media downloads (100 MB).
const MaxMediaDownloadSize = 100 * 1024 * 1024

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

	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
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

	if fileLength > MaxMediaDownloadSize {
		return 0, fmt.Errorf("media too large (%d bytes); maximum download size is %d bytes", fileLength, MaxMediaDownloadSize)
	}
	length := -1
	if fileLength > 0 {
		length = int(fileLength)
	}

	if err := cli.DownloadMediaWithPathToFile(ctx, directPath, encFileHash, fileHash, mediaKey, length, mt, mmsType, tmpFile); err != nil {
		return 0, err
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

	info, err := os.Stat(targetPath)
	if err != nil {
		return 0, fmt.Errorf("stat output file: %w", err)
	}
	return info.Size(), nil
}
