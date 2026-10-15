package main

import (
	"context"
	"fmt"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
)

const mediaDownloadKind = "media_download"

// Keep the download envelope compatible; source explains whether bytes were
// reused, obtained from the server, or recovered from the primary phone.
type mediaDownloadResult struct {
	Chat         string `json:"chat"`
	ID           string `json:"id"`
	Path         string `json:"path"`
	Bytes        int64  `json:"bytes"`
	MediaType    string `json:"media_type"`
	MIMEType     string `json:"mime_type"`
	Downloaded   bool   `json:"downloaded"`
	Source       string `json:"source"`
	ReadOnly     bool   `json:"read_only,omitempty"`
	Recorded     bool   `json:"recorded"`
	DownloadedAt string `json:"downloaded_at,omitempty"`
}

func mediaResult(info store.MediaDownloadInfo, path string, bytes int64, source string) *mediaDownloadResult {
	return &mediaDownloadResult{Chat: info.ChatJID, ID: info.MsgID, Path: path, Bytes: bytes,
		MediaType: info.MediaType, MIMEType: info.MimeType, Downloaded: true, Source: source}
}

func downloadExactMedia(ctx context.Context, flags *rootFlags, chat, id, output string, retry bool) (*mediaDownloadResult, error) {
	// Cache inspection must precede any writer/session lock. A read-only app
	// reads only the message index, even for a writable download invocation.
	roFlags := *flags
	roFlags.readOnly = true
	reader, _, preflightErr := newApp(ctx, &roFlags, false, false)
	var info store.MediaDownloadInfo
	var target string
	if preflightErr == nil {
		defer reader.Close()
		info, preflightErr = reader.DB().GetMediaDownloadInfo(chat, id)
		if preflightErr == nil {
			target, preflightErr = reader.ResolveMediaOutputPath(info, output)
		}
		if preflightErr == nil {
			bytes, hit, err := app.CopyVerifiedMediaCache(ctx, info, target)
			if err != nil {
				return nil, err
			}
			if hit {
				result := mediaResult(info, target, bytes, "cache")
				result.ReadOnly = flags.isReadOnly()
				return result, nil
			}
		}
	}
	if flags.isReadOnly() {
		if preflightErr != nil {
			return nil, preflightErr
		}
		if err := requireMediaMetadata(info); err != nil {
			return nil, err
		}
		bytes, err := wa.DownloadMediaDirectToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, target)
		if err != nil {
			return nil, err
		}
		result := mediaResult(info, target, bytes, "server")
		result.ReadOnly = true
		return result, nil
	}

	// A running owner already owns the writable session. Delegate rather than
	// stopping it, stealing its lock, or opening another authenticated writer.
	a, lk, err := newApp(ctx, flags, true, false)
	var result *mediaDownloadResult
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: mediaDownloadKind, To: chat, ID: id, RetryOnPhone: retry})
		if !delegated {
			if lock.IsLocked(delegateErr) {
				return nil, fmt.Errorf("%w; cached/read-only downloads use --read-only --output PATH; phone recovery needs a sync owner supporting media delegation", delegateErr)
			}
			return nil, delegateErr
		}
		if delegateErr != nil {
			return nil, delegateErr
		}
		result = resp.Media
		if result == nil || result.Chat != chat || result.ID != id {
			return nil, fmt.Errorf("sync owner returned a mismatched media result")
		}
	} else {
		defer closeApp(a, lk)
		if err := a.EnsureAuthed(ctx); err != nil {
			return nil, err
		}
		if err := a.Connect(ctx, false, nil); err != nil {
			return nil, err
		}
		result, err = executeExactMediaDownload(ctx, a, chat, id, retry)
		if err != nil {
			return nil, err
		}
	}
	// Only canonical cache paths cross the owner endpoint. The caller verifies
	// those bytes again and publishes its own output, never an owner-chosen file.
	if preflightErr != nil {
		roReader, _, err := newApp(ctx, &roFlags, false, false)
		if err != nil {
			return nil, err
		}
		defer roReader.Close()
		info, err = roReader.DB().GetMediaDownloadInfo(chat, id)
		if err != nil {
			return nil, err
		}
		target, err = roReader.ResolveMediaOutputPath(info, output)
		if err != nil {
			return nil, err
		}
	}
	info.LocalPath = result.Path
	bytes, hit, err := app.CopyVerifiedMediaCache(ctx, info, target)
	if err != nil {
		return nil, err
	}
	if !hit {
		return nil, fmt.Errorf("downloaded media failed local verification; no output published")
	}
	result.Path, result.Bytes = target, bytes
	return result, nil
}

func requireMediaMetadata(info store.MediaDownloadInfo) error {
	if info.MediaType == "" || info.DirectPath == "" || len(info.MediaKey) == 0 {
		return fmt.Errorf("message has no downloadable media metadata (run `wacli sync` first)")
	}
	return nil
}

// This narrow boundary permits a protocol fake in tests while exercising the
// real SQLite cache and output behavior used by both direct and owner requests.
type exactMediaDownloadApp interface {
	DB() *store.DB
	WA() app.WAClient
	ResolveMediaOutputPath(store.MediaDownloadInfo, string) (string, error)
	RetryMediaMessage(context.Context, string, string, time.Duration) (app.MediaRetryOutcome, error)
}

func executeExactMediaDownload(ctx context.Context, a exactMediaDownloadApp, chat, id string, retry bool) (*mediaDownloadResult, error) {
	info, err := a.DB().GetMediaDownloadInfo(chat, id)
	if err != nil {
		return nil, err
	}
	target, err := a.ResolveMediaOutputPath(info, "")
	if err != nil {
		return nil, err
	}
	// Recheck after media serialization: another request may have recovered it
	// while this caller was waiting. A cache hit sends no phone request.
	bytes, hit, err := app.CopyVerifiedMediaCache(ctx, info, target)
	if err != nil {
		return nil, err
	}
	if hit {
		return mediaResult(info, target, bytes, "cache"), nil
	}
	if err := requireMediaMetadata(info); err != nil {
		return nil, err
	}
	client := a.WA()
	// A daemon can expose its socket before its first successful connection.
	// Refuse that transient state; never panic or create a second client here.
	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("sync owner is not connected for media download")
	}
	bytes, err = client.DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", target)
	source := "server"
	if err != nil && retry && app.IsExpiredMediaDownload(err) {
		outcome, recoveryErr := a.RetryMediaMessage(ctx, chat, id, 30*time.Second)
		if recoveryErr != nil {
			return nil, fmt.Errorf("exact media recovery: %w", recoveryErr)
		}
		if outcome.ChatJID != chat || outcome.MsgID != id {
			return nil, fmt.Errorf("mismatched exact media recovery response")
		}
		if outcome.Status != "recovered" {
			return nil, fmt.Errorf("media recovery %s for %s/%s: %s", outcome.Status, chat, id, outcome.Detail)
		}
		target, bytes, err = outcome.Path, outcome.Bytes, nil
		source = "phone"
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if source == "server" {
		if err := a.DB().MarkMediaDownloaded(chat, id, target, now); err != nil {
			return nil, fmt.Errorf("record media download: %w", err)
		}
	}
	result := mediaResult(info, target, bytes, source)
	result.Recorded = true
	result.DownloadedAt = now.Format(time.RFC3339Nano)
	return result, nil
}
