package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Chat struct {
	JID           string    `json:"jid"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	LastMessageTS time.Time `json:"last_message_ts"`
	Archived      bool      `json:"archived"`
	Pinned        bool      `json:"pinned"`
	MutedUntil    int64     `json:"muted_until"`
	Unread        bool      `json:"unread"`
}

func (c Chat) Muted() bool {
	return c.MutedUntil == -1 || (c.MutedUntil > 0 && time.Now().Unix() < c.MutedUntil)
}

type Group struct {
	JID             string
	Name            string
	OwnerJID        string
	IsParent        bool
	LinkedParentJID string
	CreatedAt       time.Time
	LeftAt          time.Time
	UpdatedAt       time.Time
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

type Button struct {
	Type        string `json:"type"`
	DisplayText string `json:"display_text"`
	ID          string `json:"id,omitempty"`
	URL         string `json:"url,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Description string `json:"description,omitempty"`
}

type Message struct {
	ChatJID         string
	ChatName        string
	MsgID           string
	SenderJID       string
	SenderName      string
	Timestamp       time.Time
	FromMe          bool
	Text            string
	DisplayText     string
	Buttons         []Button `json:",omitempty"`
	IsForwarded     bool
	ForwardingScore uint32
	ReactionToID    string
	ReactionEmoji   string
	MediaType       string
	MediaCaption    string
	Filename        string
	MimeType        string
	DirectPath      string
	LocalPath       string
	DownloadedAt    time.Time
	Starred         bool
	StarredAt       time.Time
	Revoked         bool
	DeletedForMe    bool
	Snippet         string
	rowID           int64
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
	JID        string    `json:"jid"`
	Phone      string    `json:"phone"`
	Name       string    `json:"name"`
	Alias      string    `json:"alias"`
	SystemName string    `json:"system_name"`
	Tags       []string  `json:"tags,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
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

const DeletedMessageDisplayText = "This message was deleted"
const DeletedForMeMessageDisplayText = "This message was deleted for me"

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
