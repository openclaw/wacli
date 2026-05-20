package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	UnreadCount   int       `json:"unread_count"`
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

type CallParticipant struct {
	JID     string `json:"jid"`
	Outcome string `json:"outcome,omitempty"`
}

type CallEvent struct {
	ChatJID      string            `json:"chat_jid"`
	ChatName     string            `json:"chat_name,omitempty"`
	SenderJID    string            `json:"sender_jid,omitempty"`
	SenderName   string            `json:"sender_name,omitempty"`
	CallID       string            `json:"call_id"`
	MsgID        string            `json:"msg_id,omitempty"`
	EventType    string            `json:"event_type"`
	Direction    string            `json:"direction,omitempty"`
	Media        string            `json:"media,omitempty"`
	Outcome      string            `json:"outcome,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	CallType     string            `json:"call_type,omitempty"`
	DurationSecs int64             `json:"duration_secs,omitempty"`
	Timestamp    time.Time         `json:"timestamp"`
	Participants []CallParticipant `json:"participants,omitempty"`
	rowID        int64
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

func boolToInt64(b bool) int64 {
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

func nullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullStringIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func sqlString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

func sqlInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case []byte:
		var n int64
		_, _ = fmt.Sscan(string(x), &n)
		return n
	default:
		var n int64
		_, _ = fmt.Sscan(fmt.Sprint(x), &n)
		return n
	}
}

func storeCtx() context.Context {
	return context.Background()
}

func (d *DB) HasFTS() bool { return d.ftsEnabled }

const DeletedMessageDisplayText = "This message was deleted"
const DeletedForMeMessageDisplayText = "This message was deleted for me"

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
