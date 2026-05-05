package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

const maxAuthConnectAttempts = 3

type SyncMode string

const (
	SyncModeBootstrap SyncMode = "bootstrap"
	SyncModeOnce      SyncMode = "once"
	SyncModeFollow    SyncMode = "follow"
)

type SyncOptions struct {
	Mode            SyncMode
	AllowQR         bool
	OnQRCode        func(string)
	PairPhoneNumber string
	OnPairCode      func(string)
	AfterConnect    func(context.Context) error
	DownloadMedia   bool
	RefreshContacts bool
	RefreshGroups   bool
	IdleExit        time.Duration // only used for bootstrap/once
	MaxReconnect    time.Duration // max time to attempt reconnection before giving up (0 = unlimited)
	MaxMessages     int64         // 0 = unlimited
	MaxDBSizeBytes  int64         // 0 = unlimited
	WarnNoLimits    bool
	Verbosity       int // future
}

type SyncResult struct {
	MessagesStored int64
}

func (a *App) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	if opts.Mode == "" {
		opts.Mode = SyncModeFollow
	}
	if (opts.Mode == SyncModeBootstrap || opts.Mode == SyncModeOnce) && opts.IdleExit <= 0 {
		opts.IdleExit = 30 * time.Second
	}
	if opts.WarnNoLimits && opts.MaxMessages <= 0 && opts.MaxDBSizeBytes <= 0 {
		a.emitWarning(
			"sync_storage_uncapped",
			"warning: sync storage is uncapped; use --max-messages or --max-db-size to bound local history growth",
			nil,
		)
	}
	if err := a.checkSyncStorageLimits(opts); err != nil {
		return SyncResult{}, err
	}

	syncCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	limits := &syncStorageLimits{app: a, opts: opts, cancel: cancel}

	if err := a.OpenWA(); err != nil {
		return SyncResult{}, err
	}

	var messagesStored atomic.Int64
	lastEvent := atomic.Int64{}
	lastEvent.Store(nowUTC().UnixNano())

	disconnected := make(chan struct{}, 1)

	var stopMedia func()
	var mediaJobs chan mediaJob
	enqueueMedia := func(chatJID, msgID string) {}
	if opts.DownloadMedia {
		mediaJobs = make(chan mediaJob, 512)
		enqueueMedia = newMediaEnqueuer(syncCtx, mediaJobs)
	}

	handlerID := a.addSyncEventHandler(syncCtx, opts, &messagesStored, &lastEvent, disconnected, enqueueMedia, limits)
	defer a.wa.RemoveEventHandler(handlerID)

	if opts.DownloadMedia {
		var err error
		stopMedia, err = a.runMediaWorkers(syncCtx, mediaJobs, 4)
		if err != nil {
			return SyncResult{}, err
		}
		defer stopMedia()
	}

	if err := a.connectForSync(syncCtx, opts); err != nil {
		return SyncResult{}, err
	}
	lastEvent.Store(nowUTC().UnixNano())
	if err := a.migrateHistoricalLIDs(syncCtx); err != nil {
		return SyncResult{MessagesStored: messagesStored.Load()}, err
	}

	// Optional: bootstrap imports (helps contacts/groups management without waiting for events).
	if opts.RefreshContacts {
		_ = a.refreshContacts(syncCtx)
	}
	if opts.RefreshGroups {
		_ = a.refreshGroups(syncCtx)
	}
	if opts.AfterConnect != nil {
		if err := opts.AfterConnect(syncCtx); err != nil {
			return SyncResult{MessagesStored: messagesStored.Load()}, err
		}
	}

	var err error
	if opts.Mode == SyncModeFollow {
		_, err = a.runSyncFollow(syncCtx, opts.MaxReconnect, &messagesStored, disconnected)
	} else {
		_, err = a.runSyncUntilIdle(syncCtx, opts.IdleExit, opts.MaxReconnect, &messagesStored, &lastEvent, disconnected)
	}
	if limitErr := limits.Err(); limitErr != nil {
		return SyncResult{MessagesStored: messagesStored.Load()}, limitErr
	}
	if err != nil {
		return SyncResult{MessagesStored: messagesStored.Load()}, err
	}
	return SyncResult{MessagesStored: messagesStored.Load()}, nil
}

func (a *App) connectForSync(ctx context.Context, opts SyncOptions) error {
	connectOpts := wa.ConnectOptions{
		AllowQR:         opts.AllowQR,
		OnQRCode:        opts.OnQRCode,
		PairPhoneNumber: opts.PairPhoneNumber,
		OnPairCode:      opts.OnPairCode,
	}

	attempts := 1
	if opts.AllowQR || opts.PairPhoneNumber != "" {
		attempts = maxAuthConnectAttempts
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		err := a.wa.Connect(ctx, connectOpts)
		if err == nil {
			return nil
		}
		if attempt == attempts || ctx.Err() != nil || !isRetryableAuthConnectError(err) {
			return err
		}
		a.emitWarning(
			"auth_connect_retry",
			fmt.Sprintf("warning: auth connection dropped before pairing completed; retrying (%d/%d)", attempt+1, attempts),
			map[string]any{"attempt": attempt + 1, "attempts": attempts},
		)
		select {
		case <-time.After(authConnectRetryDelay(attempt)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func authConnectRetryDelay(attempt int) time.Duration {
	return time.Duration(attempt) * 500 * time.Millisecond
}

func isRetryableAuthConnectError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"qr code timed out",
		"qr channel closed",
		"websocket",
		"failed to read frame header",
		"connection reset",
		"broken pipe",
		"i/o timeout",
		"eof",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func (a *App) checkSyncStorageLimits(opts SyncOptions) error {
	if opts.MaxMessages > 0 {
		count, err := a.db.CountMessages()
		if err != nil {
			return fmt.Errorf("check message limit: %w", err)
		}
		if count >= opts.MaxMessages {
			return syncStorageLimitError("message", count, opts.MaxMessages)
		}
	}
	if opts.MaxDBSizeBytes > 0 {
		size, err := a.dbDiskSize()
		if err != nil {
			return fmt.Errorf("check database size limit: %w", err)
		}
		if size >= opts.MaxDBSizeBytes {
			return syncStorageLimitError("database size", size, opts.MaxDBSizeBytes)
		}
	}
	return nil
}

func (a *App) dbDiskSize() (int64, error) {
	var total int64
	for _, path := range []string{
		filepath.Join(a.opts.StoreDir, "wacli.db"),
		filepath.Join(a.opts.StoreDir, "wacli.db-wal"),
		filepath.Join(a.opts.StoreDir, "wacli.db-shm"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		if !info.IsDir() {
			total += info.Size()
		}
	}
	return total, nil
}

func syncStorageLimitError(kind string, got, limit int64) error {
	return fmt.Errorf("sync storage limit reached: %s is %d, limit is %d", kind, got, limit)
}

func chatKind(chat types.JID) string {
	if chat.Server == types.GroupServer {
		return "group"
	}
	if chat.IsBroadcastList() {
		return "broadcast"
	}
	if chat.Server == types.DefaultUserServer {
		return "dm"
	}
	return "unknown"
}

func (a *App) storeParsedMessage(ctx context.Context, pm wa.ParsedMessage) error {
	pm.Chat = a.canonicalStoreJID(ctx, pm.Chat)
	chatJID := canonicalJIDString(pm.Chat)
	chatName := a.wa.ResolveChatName(ctx, pm.Chat, pm.PushName)
	if err := a.db.UpsertChat(chatJID, chatKind(pm.Chat), chatName, pm.Timestamp); err != nil {
		return err
	}

	// Best-effort: store contact info for DMs.
	if pm.Chat.Server == types.DefaultUserServer {
		chat := canonicalJID(pm.Chat)
		if info, err := a.wa.GetContact(ctx, chat); err == nil {
			_ = a.db.UpsertContact(
				chat.String(),
				chat.User,
				info.PushName,
				info.FullName,
				info.FirstName,
				info.BusinessName,
			)
		}
	}

	senderName := ""
	if pm.FromMe {
		senderName = "me"
	} else if s := strings.TrimSpace(pm.PushName); s != "" && s != "-" {
		senderName = s
	}
	senderJID := pm.SenderJID
	if pm.SenderJID != "" {
		if jid, err := types.ParseJID(pm.SenderJID); err == nil {
			contactJID := a.canonicalStoreJID(ctx, jid)
			senderJID = contactJID.String()
			if info, err := a.wa.GetContact(ctx, contactJID); err == nil {
				if name := wa.BestContactName(info); name != "" {
					senderName = name
				}
				_ = a.db.UpsertContact(
					contactJID.String(),
					contactJID.User,
					info.PushName,
					info.FullName,
					info.FirstName,
					info.BusinessName,
				)
			}
		}
	}

	// Best-effort: store group metadata (and participants) when available.
	if pm.Chat.Server == types.GroupServer {
		if gi, err := a.wa.GetGroupInfo(ctx, pm.Chat); err == nil && gi != nil {
			_ = a.db.UpsertGroup(gi.JID.String(), gi.GroupName.Name, gi.OwnerJID.String(), gi.GroupCreated)
			var ps []store.GroupParticipant
			for _, p := range gi.Participants {
				role := "member"
				if p.IsSuperAdmin {
					role = "superadmin"
				} else if p.IsAdmin {
					role = "admin"
				}
				ps = append(ps, store.GroupParticipant{
					GroupJID: pm.Chat.String(),
					UserJID:  canonicalJIDString(p.JID),
					Role:     role,
				})
			}
			_ = a.db.ReplaceGroupParticipants(pm.Chat.String(), ps)
		}
	}

	var mediaType, caption, filename, mimeType, directPath string
	var mediaKey, fileSha, fileEncSha []byte
	var fileLen uint64
	if pm.Media != nil {
		mediaType = pm.Media.Type
		caption = pm.Media.Caption
		filename = pm.Media.Filename
		mimeType = pm.Media.MimeType
		directPath = pm.Media.DirectPath
		mediaKey = pm.Media.MediaKey
		fileSha = pm.Media.FileSHA256
		fileEncSha = pm.Media.FileEncSHA256
		fileLen = pm.Media.FileLength
	}

	displayText := a.buildDisplayText(ctx, pm)

	if err := a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:         chatJID,
		ChatName:        chatName,
		MsgID:           pm.ID,
		SenderJID:       senderJID,
		SenderName:      senderName,
		Timestamp:       pm.Timestamp,
		FromMe:          pm.FromMe,
		Text:            pm.Text,
		DisplayText:     displayText,
		IsForwarded:     pm.IsForwarded,
		ForwardingScore: pm.ForwardingScore,
		ReactionToID:    pm.ReactionToID,
		ReactionEmoji:   pm.ReactionEmoji,
		MediaType:       mediaType,
		MediaCaption:    caption,
		Filename:        filename,
		MimeType:        mimeType,
		DirectPath:      directPath,
		MediaKey:        mediaKey,
		FileSHA256:      fileSha,
		FileEncSHA256:   fileEncSha,
		FileLength:      fileLen,
	}); err != nil {
		return err
	}
	if pm.StarredKnown {
		return a.db.SetStarred(store.SetStarredParams{
			ChatJID:   chatJID,
			MsgID:     pm.ID,
			SenderJID: senderJID,
			FromMe:    pm.FromMe,
			Starred:   pm.Starred,
			StarredAt: pm.Timestamp,
		})
	}
	return nil
}

func (a *App) buildDisplayText(ctx context.Context, pm wa.ParsedMessage) string {
	base := baseDisplayText(pm)

	if pm.ReactionToID != "" || strings.TrimSpace(pm.ReactionEmoji) != "" {
		target := strings.TrimSpace(pm.ReactionToID)
		display := ""
		if target != "" {
			display = a.lookupMessageDisplayText(pm.Chat.String(), target)
		}
		if display == "" {
			display = "message"
		}
		emoji := strings.TrimSpace(pm.ReactionEmoji)
		if emoji != "" {
			return fmt.Sprintf("Reacted %s to %s", emoji, display)
		}
		return fmt.Sprintf("Reacted to %s", display)
	}

	if pm.ReplyToID != "" {
		quoted := strings.TrimSpace(pm.ReplyToDisplay)
		if quoted == "" {
			quoted = a.lookupMessageDisplayText(pm.Chat.String(), pm.ReplyToID)
		}
		if quoted == "" {
			quoted = "message"
		}
		if base == "" {
			base = "(message)"
		}
		return fmt.Sprintf("> %s\n%s", quoted, base)
	}

	if base == "" {
		base = "(message)"
	}
	return base
}

func baseDisplayText(pm wa.ParsedMessage) string {
	if pm.Media != nil {
		return "Sent " + mediaLabel(pm.Media.Type)
	}
	if text := strings.TrimSpace(pm.Text); text != "" {
		return text
	}
	return ""
}

func (a *App) lookupMessageDisplayText(chatJID, msgID string) string {
	if strings.TrimSpace(chatJID) == "" || strings.TrimSpace(msgID) == "" {
		return ""
	}
	msg, err := a.db.GetMessage(chatJID, msgID)
	if err != nil {
		return ""
	}
	if text := strings.TrimSpace(msg.DisplayText); text != "" {
		return text
	}
	if text := strings.TrimSpace(msg.Text); text != "" {
		return text
	}
	if strings.TrimSpace(msg.MediaType) != "" {
		return "Sent " + mediaLabel(msg.MediaType)
	}
	return ""
}

func mediaLabel(mediaType string) string {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	switch mt {
	case "gif":
		return "gif"
	case "image":
		return "image"
	case "video":
		return "video"
	case "audio":
		return "audio"
	case "sticker":
		return "sticker"
	case "document":
		return "document"
	case "location":
		return "location"
	case "contact":
		return "contact"
	case "contacts":
		return "contacts"
	case "":
		return "message"
	default:
		return mt
	}
}
