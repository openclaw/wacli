package store

import (
	"context"
	"time"

	"github.com/openclaw/wacli/internal/store/storedb"
)

func (d *DB) GetMediaDownloadInfo(chatJID, msgID string) (MediaDownloadInfo, error) {
	row, err := d.q.GetMediaDownloadInfo(storeCtx(), storedb.GetMediaDownloadInfoParams{ChatJid: chatJID, MsgID: msgID})
	if err != nil {
		return MediaDownloadInfo{}, err
	}
	info := MediaDownloadInfo{
		ChatJID:       row.ChatJid,
		ChatName:      row.Name,
		MsgID:         row.MsgID,
		MediaType:     row.MediaType,
		Filename:      row.Filename,
		MimeType:      row.MimeType,
		DirectPath:    row.DirectPath,
		MediaKey:      row.MediaKey,
		FileSHA256:    row.FileSha256,
		FileEncSHA256: row.FileEncSha256,
		LocalPath:     row.LocalPath,
		DownloadedAt:  fromUnix(row.DownloadedAt),
	}
	if row.FileLength > 0 {
		info.FileLength = uint64(row.FileLength)
	}
	return info, nil
}

// PendingMediaDownload identifies a message whose media has downloadable
// metadata (direct_path + media_key) but has not yet been fetched to disk.
type PendingMediaDownload struct {
	ChatJID string
	MsgID   string
}

// CountPendingMediaDownloads returns how many stored messages have downloadable
// media that has not been fetched yet. Pass a non-empty chatJID to scope the
// count to a single chat.
func (d *DB) CountPendingMediaDownloads(ctx context.Context, chatJID string) (int, error) {
	n, err := d.q.CountPendingMediaDownloads(ctx, chatJID)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// ListPendingMediaDownloads returns messages with downloadable but not-yet-fetched
// media, newest first. Pass a non-empty chatJID to scope to a single chat, and a
// positive limit to cap the number of rows (limit <= 0 means no limit).
func (d *DB) ListPendingMediaDownloads(ctx context.Context, chatJID string, limit int) ([]PendingMediaDownload, error) {
	rows, err := d.q.ListPendingMediaDownloads(ctx, storedb.ListPendingMediaDownloadsParams{
		ChatJid:    chatJID,
		LimitCount: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	pending := make([]PendingMediaDownload, 0, len(rows))
	for _, row := range rows {
		pending = append(pending, PendingMediaDownload{ChatJID: row.ChatJid, MsgID: row.MsgID})
	}
	return pending, nil
}

func (d *DB) MarkMediaDownloaded(chatJID, msgID, localPath string, downloadedAt time.Time) error {
	return d.q.MarkMediaDownloaded(storeCtx(), storedb.MarkMediaDownloadedParams{
		LocalPath:    nullStringIfEmpty(localPath),
		DownloadedAt: sqlNullInt64(unix(downloadedAt)),
		ChatJid:      chatJID,
		MsgID:        msgID,
	})
}
