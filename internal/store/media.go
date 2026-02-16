package store

import (
	"database/sql"
	"time"
)

func (d *DB) GetMediaDownloadInfo(chatJID, msgID string) (MediaDownloadInfo, error) {
	row := d.sql.QueryRow(`
		SELECT m.chat_jid,
		       COALESCE(c.name,''),
		       m.msg_id,
		       COALESCE(m.media_type,''),
		       COALESCE(m.filename,''),
		       COALESCE(m.mime_type,''),
		       COALESCE(m.direct_path,''),
		       m.media_key,
		       m.file_sha256,
		       m.file_enc_sha256,
		       COALESCE(m.file_length,0),
		       COALESCE(m.local_path,''),
		       COALESCE(m.downloaded_at,0)
		FROM messages m
		LEFT JOIN chats c ON c.jid = m.chat_jid
		WHERE m.chat_jid = ? AND m.msg_id = ?
	`, chatJID, msgID)

	var info MediaDownloadInfo
	var fileLen sql.NullInt64
	var downloadedAt int64
	if err := row.Scan(
		&info.ChatJID,
		&info.ChatName,
		&info.MsgID,
		&info.MediaType,
		&info.Filename,
		&info.MimeType,
		&info.DirectPath,
		&info.MediaKey,
		&info.FileSHA256,
		&info.FileEncSHA256,
		&fileLen,
		&info.LocalPath,
		&downloadedAt,
	); err != nil {
		return MediaDownloadInfo{}, err
	}
	if fileLen.Valid && fileLen.Int64 > 0 {
		info.FileLength = uint64(fileLen.Int64)
	}
	info.DownloadedAt = fromUnix(downloadedAt)
	return info, nil
}

func (d *DB) MarkMediaDownloaded(chatJID, msgID, localPath string, downloadedAt time.Time) error {
	_, err := d.sql.Exec(`
		UPDATE messages
		SET local_path = ?, downloaded_at = ?
		WHERE chat_jid = ? AND msg_id = ?
	`, localPath, unix(downloadedAt), chatJID, msgID)
	return err
}
