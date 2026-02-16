package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Chat struct {
	JID           string
	Kind          string
	Name          string
	LastMessageTS time.Time
}

type Group struct {
	JID       string
	Name      string
	OwnerJID  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type GroupParticipant struct {
	GroupJID  string
	UserJID   string
	Role      string
	UpdatedAt time.Time
}

type MediaDownloadInfo struct {
	ChatJID       string
	ChatName      string
	MsgID         string
	MediaType     string
	Filename      string
	MimeType      string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
	LocalPath     string
	DownloadedAt  time.Time
}

type Message struct {
	ChatJID     string
	ChatName    string
	MsgID       string
	SenderJID   string
	Timestamp   time.Time
	FromMe      bool
	Text        string
	DisplayText string
	MediaType   string
	Snippet     string
}

type MessageInfo struct {
	ChatJID    string
	MsgID      string
	Timestamp  time.Time
	FromMe     bool
	SenderJID  string
	SenderName string
}

type Contact struct {
	JID       string
	Phone     string
	Name      string
	Alias     string
	Tags      []string
	UpdatedAt time.Time
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

func fromUnix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) interface{} {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

func (d *DB) HasFTS() bool { return d.ftsEnabled }

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
