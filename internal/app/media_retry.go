package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// MediaRetryOutcome is the per-message result of a media-retry attempt.
type MediaRetryOutcome struct {
	ChatJID string `json:"chat_jid"`
	MsgID   string `json:"msg_id"`
	Status  string `json:"status"` // recovered | not_on_phone | no_response | error
	Path    string `json:"path,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// MediaRetryResult summarises a media-retry run.
type MediaRetryResult struct {
	Requested  int                 `json:"requested"`
	Recovered  int                 `json:"recovered"`
	NotOnPhone int                 `json:"not_on_phone"`
	NoResponse int                 `json:"no_response"`
	Failed     int                 `json:"failed"`
	Outcomes   []MediaRetryOutcome `json:"outcomes"`
}

// RetryMediaOptions controls a media-retry run.
type RetryMediaOptions struct {
	ChatJID    string        // scope to a single chat (optional)
	BeforeUnix int64         // only retry media older than this unix time (optional)
	BeforeSet  bool          // distinguish an explicit Unix epoch filter from no filter
	Limit      int           // cap total messages to retry (0 = all pending)
	BatchSize  int           // receipts in flight per batch (default 32)
	Wait       time.Duration // how long to wait for the phone per attempt (default 30s)
}

type retryNotif struct {
	directPath string
	code       int
	err        error
}

type mediaRetryKey struct {
	chatJID string
	msgID   string
}

// RetryMedia recovers media that expired off WhatsApp's CDN by asking the phone
// to re-upload it. It sends media-retry receipts in batches, waits for the
// phone's notifications (with one second attempt for non-responders), downloads
// whatever the phone re-served, and marks genuinely-gone media so future runs
// skip it. Recovery only works while the phone is online and still holds the
// media. Requires an authenticated, connected client.
func (a *App) RetryMedia(ctx context.Context, opts RetryMediaOptions) (MediaRetryResult, error) {
	if err := ctx.Err(); err != nil {
		return MediaRetryResult{}, err
	}
	if opts.Limit < 0 {
		return MediaRetryResult{}, fmt.Errorf("limit must be >= 0")
	}
	if opts.BatchSize < 0 {
		return MediaRetryResult{}, fmt.Errorf("batch size must be >= 0")
	}
	if opts.Wait < 0 {
		return MediaRetryResult{}, fmt.Errorf("wait must be >= 0")
	}
	opts.ChatJID = strings.TrimSpace(opts.ChatJID)
	if opts.BatchSize <= 0 {
		opts.BatchSize = 32
	}
	if opts.Wait <= 0 {
		opts.Wait = 30 * time.Second
	}

	var pending []store.PendingMediaDownload
	var err error
	if opts.BeforeSet || opts.BeforeUnix != 0 {
		pending, err = a.db.ListPendingMediaBefore(ctx, opts.ChatJID, opts.BeforeUnix, opts.Limit)
	} else {
		pending, err = a.db.ListPendingMediaDownloads(ctx, opts.ChatJID, opts.Limit)
	}
	if err != nil {
		return MediaRetryResult{}, fmt.Errorf("list pending media: %w", err)
	}
	result := MediaRetryResult{Requested: len(pending)}
	if len(pending) == 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		return result, nil
	}

	// Shared notification map, keyed by chat and message ID as echoed by the
	// phone. Message IDs are only unique within a chat.
	// Guarded by mu because the event handler runs on whatsmeow's dispatch
	// goroutine.
	var mu sync.Mutex
	infos := make(map[mediaRetryKey]store.MediaDownloadInfo, len(pending))
	notifs := make(map[mediaRetryKey]retryNotif, len(pending))

	handlerID := a.wa.AddEventHandler(func(evt interface{}) {
		mr, ok := evt.(*events.MediaRetry)
		if !ok {
			return
		}
		key := mediaRetryKey{chatJID: canonicalJIDString(a.canonicalStoreJID(ctx, mr.ChatID)), msgID: string(mr.MessageID)}
		mu.Lock()
		defer mu.Unlock()
		info, tracked := infos[key]
		if !tracked {
			return
		}
		if _, seen := notifs[key]; seen {
			return
		}
		dp, code, derr := wa.DecryptMediaRetry(mr, info.MediaKey)
		notifs[key] = retryNotif{directPath: dp, code: code, err: derr}
	})
	defer a.wa.RemoveEventHandler(handlerID)

	// Load message info + build retry receipts, dropping rows we cannot address.
	type ready struct {
		info store.MediaDownloadInfo
		mi   *types.MessageInfo
	}
	prepared := make(map[mediaRetryKey]ready, len(pending))
	orderedKeys := make([]mediaRetryKey, 0, len(pending))
	groupAddressingModes := make(map[types.JID]types.AddressingMode)
	for _, p := range pending {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		info, err := a.db.GetMediaDownloadInfo(p.ChatJID, p.MsgID)
		if err != nil {
			result.Failed++
			result.Outcomes = append(result.Outcomes, MediaRetryOutcome{ChatJID: p.ChatJID, MsgID: p.MsgID, Status: "error", Detail: fmt.Sprintf("load info: %v", err)})
			continue
		}
		msg, err := a.db.GetMessage(p.ChatJID, p.MsgID)
		if err != nil {
			result.Failed++
			result.Outcomes = append(result.Outcomes, MediaRetryOutcome{ChatJID: p.ChatJID, MsgID: p.MsgID, Status: "error", Detail: fmt.Sprintf("load message: %v", err)})
			continue
		}
		chat, err := types.ParseJID(p.ChatJID)
		if err != nil {
			result.Failed++
			result.Outcomes = append(result.Outcomes, MediaRetryOutcome{ChatJID: p.ChatJID, MsgID: p.MsgID, Status: "error", Detail: fmt.Sprintf("parse chat jid: %v", err)})
			continue
		}
		sender, senderErr := types.ParseJID(msg.SenderJID)
		isGroupLike := chat.Server == types.GroupServer || chat.IsBroadcastList()
		if isGroupLike && (senderErr != nil || sender.IsEmpty()) && msg.FromMe {
			sender, senderErr = types.ParseJID(a.wa.LinkedJID())
		}
		if isGroupLike && (senderErr != nil || sender.IsEmpty()) {
			result.Failed++
			result.Outcomes = append(result.Outcomes, MediaRetryOutcome{ChatJID: p.ChatJID, MsgID: p.MsgID, Status: "error", Detail: "group or broadcast media is missing its sender JID"})
			continue
		}
		addressingMode := types.AddressingModePN
		if chat.Server == types.GroupServer {
			var ok bool
			addressingMode, ok = groupAddressingModes[chat]
			if !ok {
				groupInfo, groupErr := a.wa.GetGroupInfo(ctx, chat)
				if groupErr != nil {
					result.Failed++
					result.Outcomes = append(result.Outcomes, MediaRetryOutcome{ChatJID: p.ChatJID, MsgID: p.MsgID, Status: "error", Detail: fmt.Sprintf("load group addressing mode: %v", groupErr)})
					continue
				}
				if groupInfo == nil {
					result.Failed++
					result.Outcomes = append(result.Outcomes, MediaRetryOutcome{ChatJID: p.ChatJID, MsgID: p.MsgID, Status: "error", Detail: "load group addressing mode: no group info returned"})
					continue
				}
				addressingMode = groupInfo.AddressingMode
				if addressingMode == "" {
					addressingMode = types.AddressingModePN
				}
				groupAddressingModes[chat] = addressingMode
			}
		}
		key := mediaRetryKey{chatJID: chat.String(), msgID: p.MsgID}
		prepared[key] = ready{info: info, mi: &types.MessageInfo{
			ID: p.MsgID,
			MessageSource: types.MessageSource{
				Chat:           chat,
				Sender:         sender,
				IsFromMe:       msg.FromMe,
				IsGroup:        isGroupLike,
				AddressingMode: addressingMode,
			},
		}}
		mu.Lock()
		infos[key] = info
		mu.Unlock()
		orderedKeys = append(orderedKeys, key)
	}

	sendReceipt := func(key mediaRetryKey) bool {
		r := prepared[key]
		if err := a.wa.SendMediaRetryReceipt(ctx, r.mi, r.info.MediaKey); err != nil {
			mu.Lock()
			notifs[key] = retryNotif{err: err}
			mu.Unlock()
			return false
		}
		return true
	}
	responded := func(keys []mediaRetryKey) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, key := range keys {
			if _, ok := notifs[key]; !ok {
				return false
			}
		}
		return true
	}
	waitForBatch := func(keys []mediaRetryKey) {
		timer := time.NewTimer(opts.Wait)
		defer timer.Stop()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			if responded(keys) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				return
			case <-tick.C:
			}
		}
	}
	missing := func(keys []mediaRetryKey) []mediaRetryKey {
		mu.Lock()
		defer mu.Unlock()
		var out []mediaRetryKey
		for _, key := range keys {
			if _, ok := notifs[key]; !ok {
				out = append(out, key)
			}
		}
		return out
	}

	// Process in batches so we never have more than BatchSize receipts in flight.
	for start := 0; start < len(orderedKeys); start += opts.BatchSize {
		if ctx.Err() != nil {
			break
		}
		end := start + opts.BatchSize
		if end > len(orderedKeys) {
			end = len(orderedKeys)
		}
		batch := orderedKeys[start:end]

		for _, key := range batch {
			sendReceipt(key)
		}
		waitForBatch(batch)
		// One second attempt for non-responders (often just timing).
		if retryIDs := missing(batch); len(retryIDs) > 0 && ctx.Err() == nil {
			for _, key := range retryIDs {
				sendReceipt(key)
			}
			waitForBatch(retryIDs)
		}

		for _, key := range batch {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			mu.Lock()
			n, got := notifs[key]
			info := infos[key]
			mu.Unlock()
			result.Outcomes = append(result.Outcomes, a.classifyRetry(ctx, info, key.msgID, n, got, &result))
		}
		a.emitEvent("media_retry_progress", map[string]any{
			"done":         end,
			"total":        len(orderedKeys),
			"recovered":    result.Recovered,
			"not_on_phone": result.NotOnPhone,
			"no_response":  result.NoResponse,
			"failed":       result.Failed,
		})
	}
	return result, ctx.Err()
}

// classifyRetry turns a single message's notification into an outcome, updating
// the aggregate counters and performing the download when the phone re-served
// the media.
func (a *App) classifyRetry(ctx context.Context, info store.MediaDownloadInfo, id string, n retryNotif, got bool, result *MediaRetryResult) MediaRetryOutcome {
	out := MediaRetryOutcome{ChatJID: info.ChatJID, MsgID: id}
	switch {
	case !got:
		out.Status = "no_response"
		result.NoResponse++
	case n.err != nil:
		out.Status = "error"
		out.Detail = n.err.Error()
		result.Failed++
	case n.code == wa.MediaRetryNotFound:
		// A phone may no longer hold media that is still available from the CDN.
		// Try the stored path before permanently excluding this row from backfill.
		path, bytes, directErr := a.downloadMediaWithDirectPath(ctx, info, info.DirectPath)
		switch {
		case directErr == nil:
			out.Status = "recovered"
			out.Path = path
			out.Bytes = bytes
			result.Recovered++
		case !isExpiredMediaDownload(directErr):
			out.Status = "error"
			out.Detail = fmt.Sprintf("phone has no copy; direct download: %v", directErr)
			result.Failed++
		default:
			if markErr := a.db.MarkMediaUnavailable(ctx, info.ChatJID, id, nowUTC()); markErr != nil {
				out.Status = "error"
				out.Detail = fmt.Sprintf("phone has no copy; mark unavailable: %v", markErr)
				result.Failed++
			} else {
				out.Status = "not_on_phone"
				result.NotOnPhone++
			}
		}
	case n.code != wa.MediaRetrySuccess || strings.TrimSpace(n.directPath) == "":
		out.Status = "error"
		out.Detail = fmt.Sprintf("retry result code %d", n.code)
		result.Failed++
	default:
		path, bytes, derr := a.downloadMediaWithDirectPath(ctx, info, n.directPath)
		if derr != nil {
			out.Status = "error"
			out.Detail = fmt.Sprintf("download: %v", derr)
			result.Failed++
		} else {
			out.Status = "recovered"
			out.Path = path
			out.Bytes = bytes
			result.Recovered++
		}
	}
	return out
}

func isExpiredMediaDownload(err error) bool {
	return errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
}

// downloadMediaWithDirectPath downloads media using a caller-supplied direct
// path (e.g. a fresh one from a media-retry notification) rather than the one
// stored in the DB, then records the local path.
func (a *App) downloadMediaWithDirectPath(ctx context.Context, info store.MediaDownloadInfo, directPath string) (string, int64, error) {
	targetPath, err := a.ResolveMediaOutputPath(info, "")
	if err != nil {
		return "", 0, err
	}
	if err := fsutil.EnsurePrivateDir(filepath.Dir(targetPath)); err != nil {
		return "", 0, err
	}
	n, err := a.wa.DownloadMediaToFile(ctx, directPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", targetPath)
	if err != nil {
		return "", 0, err
	}
	if err := a.db.MarkMediaDownloaded(info.ChatJID, info.MsgID, targetPath, nowUTC()); err != nil {
		return "", 0, err
	}
	return targetPath, n, nil
}
