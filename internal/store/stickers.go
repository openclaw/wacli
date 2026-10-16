package store

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// The *_ts columns hold Unix milliseconds, because favorite mutations ordered
// newest-wins can land within one second. A NULL favorite_ts means only the
// pairing history sync listed the sticker. recent_removed_ts keeps a removal
// that arrives before that history sync.
const stickersSchema = `
 CREATE TABLE IF NOT EXISTS stickers (
  file_hash TEXT PRIMARY KEY,
  favorite INTEGER NOT NULL DEFAULT 0,
  favorite_ts INTEGER,
  recent INTEGER NOT NULL DEFAULT 0,
  recent_weight REAL,
  last_sent_ts INTEGER,
  recent_removed_ts INTEGER,
  direct_path TEXT,
  media_key BLOB,
  file_enc_sha256 BLOB,
  file_length INTEGER,
  mime_type TEXT,
  width INTEGER,
  height INTEGER,
  is_lottie INTEGER NOT NULL DEFAULT 0,
  is_avatar INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
 );
`

func ensureStickersTable(d *DB) error {
	if _, err := d.sql.Exec(stickersSchema); err != nil {
		return fmt.Errorf("ensure stickers: %w", err)
	}
	return nil
}

type Sticker struct {
	FileHash     string       `json:"file_hash"`
	Favorite     bool         `json:"favorite"`
	FavoritedAt  *time.Time   `json:"favorited_at,omitempty"`
	Recent       bool         `json:"recent"`
	RecentWeight float64      `json:"recent_weight,omitempty"`
	LastSentAt   *time.Time   `json:"last_sent_at,omitempty"`
	MimeType     string       `json:"mime_type,omitempty"`
	Width        int64        `json:"width,omitempty"`
	Height       int64        `json:"height,omitempty"`
	FileLength   uint64       `json:"file_length,omitempty"`
	IsLottie     bool         `json:"is_lottie,omitempty"`
	IsAvatar     bool         `json:"is_avatar,omitempty"`
	Downloadable bool         `json:"downloadable"`
	UpdatedAt    time.Time    `json:"updated_at"`
	Media        StickerMedia `json:"-"`
}

type StickerMedia struct {
	DirectPath    string
	MediaKey      []byte
	FileEncSHA256 []byte
	// Decoded from the file hash: favorite mutations carry no plaintext digest.
	FileSHA256 []byte
}

type StickerParams struct {
	FileHash      string
	DirectPath    string
	MediaKey      []byte
	FileEncSHA256 []byte
	MimeType      string
	Width         uint32
	Height        uint32
	FileLength    uint64
	IsLottie      bool
	IsAvatar      bool
}

type ListStickersParams struct {
	Favorites bool
	Recent    bool
	Limit     int
}

var stickerDescriptorColumns = []string{
	"direct_path", "media_key", "file_enc_sha256", "file_length",
	"mime_type", "width", "height", "is_lottie", "is_avatar",
}

func (p StickerParams) descriptorValues() []any {
	if strings.TrimSpace(p.DirectPath) == "" || len(p.MediaKey) == 0 || len(p.FileEncSHA256) == 0 {
		return []any{nil, nil, nil, nil, nil, nil, nil, 0, 0}
	}
	return []any{
		strings.TrimSpace(p.DirectPath), p.MediaKey, p.FileEncSHA256, int64(p.FileLength),
		nullIfEmpty(p.MimeType), int64(p.Width), int64(p.Height), boolToInt64(p.IsLottie), boolToInt64(p.IsAvatar),
	}
}

// The descriptor is replaced as a unit, and only by a complete one, because a
// media key decrypts only its own upload.
func stickerDescriptorUpdate(when string) string {
	assignments := make([]string, 0, len(stickerDescriptorColumns))
	for _, col := range stickerDescriptorColumns {
		assignments = append(assignments, fmt.Sprintf(
			"%[1]s = CASE WHEN excluded.direct_path IS NOT NULL AND (%[2]s) THEN excluded.%[1]s ELSE stickers.%[1]s END",
			col, when))
	}
	return strings.Join(assignments, ",\n\t\t\t")
}

func stickerMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

func stickerTime(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

func stickerFileHash(fileHash string) (string, error) {
	fileHash = strings.TrimSpace(fileHash)
	if fileHash == "" {
		return "", fmt.Errorf("sticker file hash is required")
	}
	return fileHash, nil
}

// Full replays and live updates arrive in either order; the newer one wins.
func (d *DB) SetFavoriteSticker(p StickerParams, favorite bool, changedAt time.Time) error {
	fileHash, err := stickerFileHash(p.FileHash)
	if err != nil {
		return err
	}
	args := append([]any{fileHash, boolToInt64(favorite), stickerMillis(changedAt)}, p.descriptorValues()...)
	args = append(args, nowUTC().Unix())
	_, err = d.sql.Exec(`
		INSERT INTO stickers(file_hash, favorite, favorite_ts, `+strings.Join(stickerDescriptorColumns, ", ")+`, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_hash) DO UPDATE SET
			favorite = CASE WHEN excluded.favorite_ts >= COALESCE(stickers.favorite_ts, 0) THEN excluded.favorite ELSE stickers.favorite END,
			favorite_ts = MAX(excluded.favorite_ts, COALESCE(stickers.favorite_ts, 0)),
			`+stickerDescriptorUpdate("excluded.favorite_ts >= COALESCE(stickers.favorite_ts, 0) OR stickers.direct_path IS NULL")+`,
			updated_at = excluded.updated_at
	`, args...)
	return err
}

// An applied favoriteSticker mutation takes precedence over the history sync.
func (d *DB) AddHistoryFavoriteSticker(p StickerParams) error {
	fileHash, err := stickerFileHash(p.FileHash)
	if err != nil {
		return err
	}
	args := append([]any{fileHash}, p.descriptorValues()...)
	args = append(args, nowUTC().Unix())
	_, err = d.sql.Exec(`
		INSERT INTO stickers(file_hash, favorite, `+strings.Join(stickerDescriptorColumns, ", ")+`, updated_at)
		VALUES(?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_hash) DO UPDATE SET
			favorite = CASE WHEN stickers.favorite_ts IS NULL THEN 1 ELSE stickers.favorite END,
			`+stickerDescriptorUpdate("stickers.direct_path IS NULL")+`,
			updated_at = excluded.updated_at
	`, args...)
	return err
}

func (d *DB) AddHistoryRecentSticker(p StickerParams, weight float32, lastSentAt time.Time) error {
	fileHash, err := stickerFileHash(p.FileHash)
	if err != nil {
		return err
	}
	args := append([]any{fileHash, float64(weight), stickerMillis(lastSentAt)}, p.descriptorValues()...)
	args = append(args, nowUTC().Unix())
	_, err = d.sql.Exec(`
		INSERT INTO stickers(file_hash, recent, recent_weight, last_sent_ts, `+strings.Join(stickerDescriptorColumns, ", ")+`, updated_at)
		VALUES(?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_hash) DO UPDATE SET
			recent = CASE WHEN excluded.last_sent_ts > COALESCE(stickers.recent_removed_ts, -1) THEN 1 ELSE stickers.recent END,
			recent_weight = excluded.recent_weight,
			last_sent_ts = MAX(excluded.last_sent_ts, COALESCE(stickers.last_sent_ts, 0)),
			`+stickerDescriptorUpdate("stickers.direct_path IS NULL")+`,
			updated_at = excluded.updated_at
	`, args...)
	return err
}

// A sticker sent again after lastSentAt stays recent.
func (d *DB) RemoveRecentSticker(fileHash string, lastSentAt time.Time) error {
	fileHash, err := stickerFileHash(fileHash)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`
		INSERT INTO stickers(file_hash, recent_removed_ts, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(file_hash) DO UPDATE SET
			recent = CASE WHEN COALESCE(stickers.last_sent_ts, 0) <= excluded.recent_removed_ts THEN 0 ELSE stickers.recent END,
			recent_removed_ts = MAX(excluded.recent_removed_ts, COALESCE(stickers.recent_removed_ts, 0)),
			updated_at = excluded.updated_at
	`, fileHash, stickerMillis(lastSentAt), nowUTC().Unix())
	return err
}

func (d *DB) ListStickers(p ListStickersParams) ([]Sticker, error) {
	// Read-only opens skip schema maintenance, so an older store may lack it.
	exists, err := d.tableExists("stickers")
	if err != nil || !exists {
		return nil, err
	}
	query := `
		SELECT file_hash, favorite, COALESCE(favorite_ts, 0), recent, COALESCE(recent_weight, 0),
		       COALESCE(last_sent_ts, 0), COALESCE(direct_path, ''), media_key, file_enc_sha256,
		       COALESCE(file_length, 0), COALESCE(mime_type, ''), COALESCE(width, 0), COALESCE(height, 0),
		       is_lottie, is_avatar, updated_at
		FROM stickers`
	switch {
	case p.Favorites && !p.Recent:
		query += " WHERE favorite = 1"
	case p.Recent && !p.Favorites:
		query += " WHERE recent = 1"
	default:
		query += " WHERE favorite = 1 OR recent = 1"
	}
	query += " ORDER BY favorite DESC, COALESCE(favorite_ts, 0) DESC, COALESCE(recent_weight, 0) DESC, COALESCE(last_sent_ts, 0) DESC, file_hash"
	var args []any
	if p.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, p.Limit)
	}
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Sticker
	for rows.Next() {
		var s Sticker
		var favoriteTS, lastSentTS, updatedAt, fileLength int64
		if err := rows.Scan(&s.FileHash, &s.Favorite, &favoriteTS, &s.Recent, &s.RecentWeight,
			&lastSentTS, &s.Media.DirectPath, &s.Media.MediaKey, &s.Media.FileEncSHA256,
			&fileLength, &s.MimeType, &s.Width, &s.Height, &s.IsLottie, &s.IsAvatar, &updatedAt); err != nil {
			return nil, err
		}
		if s.Favorite {
			s.FavoritedAt = stickerTime(favoriteTS)
		}
		s.LastSentAt = stickerTime(lastSentTS)
		if fileLength > 0 {
			s.FileLength = uint64(fileLength)
		}
		if digest, err := base64.StdEncoding.DecodeString(s.FileHash); err == nil && len(digest) == sha256.Size {
			s.Media.FileSHA256 = digest
		}
		// Without a digest the plaintext cannot be verified, so it is not offered.
		s.Downloadable = s.Media.DirectPath != "" && len(s.Media.MediaKey) > 0 && len(s.Media.FileEncSHA256) > 0 &&
			len(s.Media.FileSHA256) == sha256.Size
		s.UpdatedAt = fromUnix(updatedAt)
		out = append(out, s)
	}
	return out, rows.Err()
}
