package main

import (
	"context"
	"errors"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/openclaw/wacli/internal/wa"
)

const profilePictureInfoKind = "profile_picture_info"

type profilePictureInfo struct {
	JID        string `json:"jid"`
	URL        string `json:"url,omitempty"`
	ID         string `json:"id,omitempty"`
	Type       string `json:"type,omitempty"`
	DirectPath string `json:"directPath,omitempty"`
	Hash       []byte `json:"hash,omitempty"`
	Unchanged  bool   `json:"unchanged,omitempty"`
}

type profilePictureClient interface {
	LinkedJID() string
	GetProfilePictureInfo(context.Context, types.JID, bool, string) (*types.ProfilePictureInfo, error)
}

// executeProfilePictureInfo answers `profile picture-info` on the running sync
// process's connection, so a caller never needs the store lock or a second
// connection. It is a read: it bypasses the send queue and pacing (see
// handleSendDelegateConn). Failures carry a stable error_code next to the
// message: not_set, hidden, invalid_jid, timeout, unavailable, lookup_failed.
// Without a jid it reads the linked account's own picture.
func executeProfilePictureInfo(ctx context.Context, client profilePictureClient, req sendDelegateRequest) sendDelegateResponse {
	failure := func(code, message string) sendDelegateResponse {
		return sendDelegateResponse{ErrorCode: code, Error: message}
	}
	raw := req.JID
	if raw == "" {
		raw = client.LinkedJID()
		if raw == "" {
			return failure("unavailable", "account is not linked")
		}
	}
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" || strings.TrimSpace(raw) != raw ||
		(jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer && jid.Server != types.GroupServer) {
		return failure("invalid_jid", "jid must identify a user or group")
	}
	jid = jid.ToNonAD()
	info, err := client.GetProfilePictureInfo(ctx, jid, req.Preview, req.ExistingID)
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		return failure("not_set", "profile picture is not set")
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return failure("hidden", "profile picture is private")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), errors.Is(err, whatsmeow.ErrIQTimedOut):
		return failure("timeout", "profile picture lookup timed out; retry")
	case errors.Is(err, wa.ErrNotConnected):
		return failure("unavailable", "sync is not connected to WhatsApp; wait for it to reconnect and retry")
	case err != nil:
		return failure("lookup_failed", "profile picture lookup failed: "+err.Error()+"; retry, and check the sync log if it persists")
	case info == nil && req.ExistingID != "":
		return sendDelegateResponse{OK: true, ProfilePicture: &profilePictureInfo{JID: jid.String(), Unchanged: true}}
	case info == nil || info.URL == "":
		return failure("lookup_failed", "profile picture response has no URL")
	}
	return sendDelegateResponse{OK: true, ProfilePicture: &profilePictureInfo{
		JID: jid.String(), URL: info.URL, ID: info.ID, Type: info.Type, DirectPath: info.DirectPath, Hash: info.Hash,
	}}
}
