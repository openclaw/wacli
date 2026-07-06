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

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

const maxAuthConnectAttempts = 3

// MaxStaleThreshold returns the exclusive upper bound for keepalive-failure thresholds.
// It reserves one maximum keepalive probe interval plus response deadline before
// whatsmeow's own failed-keepalive auto-reconnect window.
func MaxStaleThreshold() time.Duration {
	return whatsmeow.KeepAliveMaxFailTime - whatsmeow.KeepAliveIntervalMax - whatsmeow.KeepAliveResponseDeadline
}

type SyncMode string

const (
	SyncModeBootstrap SyncMode = "bootstrap"
	SyncModeOnce      SyncMode = "once"
	SyncModeFollow    SyncMode = "follow"
)

type SyncOptions struct {
	Mode                SyncMode
	AllowQR             bool
	OnQRCode            func(string)
	PairPhoneNumber     string
	OnPairCode          func(string)
	AfterConnect        func(context.Context) error
	DownloadMedia       bool
	RefreshContacts     bool
	RefreshGroups       bool
	RefreshChannels     bool
	IdleExit            time.Duration // only used for bootstrap/once
	MaxReconnect        time.Duration // max time to attempt reconnection before giving up (0 = unlimited)
	StaleThreshold      time.Duration // force reconnect when keepalive failures last this long in follow mode (0 = disabled)
	MaxMessages         int64         // 0 = unlimited
	MaxDBSizeBytes      int64         // 0 = unlimited
	WarnNoLimits        bool
	WebhookURL          string
	WebhookSecret       string
	WebhookAllowPrivate bool
	Verbosity           int // future
}

type SyncResult struct {
	MessagesStored int64
}

func (a *App) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	status := a.beginSyncStatus()
	defer a.endSyncStatus(status)

	if opts.Mode == "" {
		opts.Mode = SyncModeFollow
	}
	if (opts.Mode == SyncModeBootstrap || opts.Mode == SyncModeOnce) && opts.IdleExit <= 0 {
		opts.IdleExit = 30 * time.Second
	}
	if maxStaleThreshold := MaxStaleThreshold(); opts.StaleThreshold >= maxStaleThreshold {
		return SyncResult{}, fmt.Errorf("stale threshold %s must be less than upstream auto-reconnect threshold %s", opts.StaleThreshold, maxStaleThreshold)
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
	if opts.Mode == SyncModeFollow && opts.StaleThreshold > 0 {
		restoreAutoReconnect, ok := a.wa.SetAutoReconnect(false)
		if !ok {
			return SyncResult{}, fmt.Errorf("could not configure stale-threshold reconnect on an already-connected WhatsApp client")
		}
		defer func() {
			if !a.wa.IsConnected() {
				a.wa.SetAutoReconnect(restoreAutoReconnect)
			}
		}()
	}
	a.wa.SetManualHistorySyncDownload(true)
	defer a.wa.SetManualHistorySyncDownload(false)

	var messagesStored atomic.Int64
	lastEvent := atomic.Int64{}
	connectionEpoch := atomic.Int64{}
	now := nowUTC().UnixNano()
	lastEvent.Store(now)

	disconnected := make(chan struct{}, 1)
	staleReconnect := make(chan staleReconnectRequest, 1)

	var waitMedia func(context.Context) bool
	var mediaQ *mediaQueue
	enqueueMedia := func(chatJID, msgID string) {}
	if opts.DownloadMedia {
		mediaQ = newMediaQueue(512)
		enqueueMedia = newMediaEnqueuer(syncCtx, mediaQ)
	}

	if opts.DownloadMedia {
		wait, cancelMedia, err := a.runMediaWorkers(syncCtx, mediaQ, 4)
		if err != nil {
			return SyncResult{}, err
		}
		defer func() {
			cancelMedia()
			wait()
		}()
		waitMedia = mediaQ.waitIdle
	}

	var stopWebhook func()
	var webhookJobs chan wa.ParsedMessage
	enqueueWebhook := func(wa.ParsedMessage) {}
	if syncWebhookEnabled(opts) {
		webhookJobs = make(chan wa.ParsedMessage, 512)
		enqueueWebhook = a.newSyncWebhookEnqueuer(syncCtx, webhookJobs)
		stopWebhook = a.runSyncWebhookWorker(syncCtx, opts, webhookJobs)
		defer stopWebhook()
	}

	ps := &syncPresence{}
	handlerID := a.addSyncEventHandler(syncCtx, opts, &messagesStored, &lastEvent, disconnected, staleReconnect, enqueueMedia, enqueueWebhook, limits, ps, mediaQ)
	defer a.wa.RemoveEventHandler(handlerID)

	connectionEpoch.Store(nowUTC().UnixNano())
	if err := a.connectForSync(syncCtx, opts); err != nil {
		return SyncResult{}, err
	}
	// Ensure unavailable presence is sent on ALL post-connect exits
	// (success, error, storage limit, reconnect failure), not just the
	// success path. The websocket stays alive via DetachSocket, so the
	// send can complete even after the sync context is cancelled.
	defer func() {
		ps.mu.Lock()
		ps.cleanupStarted = true
		ps.mu.Unlock()
		a.wa.RemoveEventHandler(handlerID)
		a.sendPresenceBounded(types.PresenceUnavailable)
	}()
	now = nowUTC().UnixNano()
	lastEvent.Store(now)
	if err := a.migrateHistoricalLIDs(syncCtx); err != nil {
		return SyncResult{MessagesStored: messagesStored.Load()}, err
	}
	a.syncAppStateDeltas(syncCtx)

	// Optional: bootstrap imports (helps contacts/groups management without waiting for events).
	if opts.RefreshContacts {
		if err := a.refreshContacts(syncCtx); err != nil {
			a.emitWarning(
				"refresh_contacts_failed",
				fmt.Sprintf("warning: failed to refresh contacts: %v", err),
				map[string]any{"error": err.Error()},
			)
		}
	}
	if opts.RefreshGroups {
		if err := a.refreshGroups(syncCtx); err != nil {
			a.emitWarning(
				"refresh_groups_failed",
				fmt.Sprintf("warning: failed to refresh groups: %v", err),
				map[string]any{"error": err.Error()},
			)
		}
	}
	if opts.RefreshChannels {
		if err := a.refreshNewsletters(syncCtx); err != nil {
			a.emitWarning(
				"refresh_channels_failed",
				fmt.Sprintf("warning: failed to refresh channels: %v", err),
				map[string]any{"error": err.Error()},
			)
		}
	}
	if opts.AfterConnect != nil {
		if err := opts.AfterConnect(syncCtx); err != nil {
			return SyncResult{MessagesStored: messagesStored.Load()}, err
		}
	}

	var err error
	if opts.Mode == SyncModeFollow {
		_, err = a.runSyncFollow(syncCtx, opts.MaxReconnect, &messagesStored, &connectionEpoch, disconnected, staleReconnect)
	} else {
		_, err = a.runSyncUntilIdle(syncCtx, opts.IdleExit, opts.MaxReconnect, &messagesStored, &lastEvent, disconnected)
	}
	limitErr := limits.Err()
	// Successful one-shot modes must finish queued downloads before cleanup
	// cancels the worker context. Follow mode keeps the queue open; error,
	// cancellation, and storage-limit exits retain immediate cancellation.
	if waitMedia != nil && opts.Mode != SyncModeFollow && err == nil && limitErr == nil && syncCtx.Err() == nil {
		waitMedia(syncCtx)
		limitErr = limits.Err()
	}
	if limitErr != nil {
		return SyncResult{MessagesStored: messagesStored.Load()}, limitErr
	}
	if err != nil {
		return SyncResult{MessagesStored: messagesStored.Load()}, err
	}
	return SyncResult{MessagesStored: messagesStored.Load()}, nil
}

func (a *App) syncAppStateDeltas(ctx context.Context) {
	for _, name := range []appstate.WAPatchName{appstate.WAPatchRegularHigh, appstate.WAPatchRegularLow, appstate.WAPatchRegular} {
		fullSync := name == appstate.WAPatchRegular
		if err := a.wa.FetchAppState(ctx, string(name), fullSync, false); err != nil {
			a.emitWarning(
				"app_state_sync_failed",
				fmt.Sprintf("warning: failed to sync WhatsApp app state %s: %v", name, err),
				map[string]any{"name": string(name), "error": err.Error()},
			)
		}
	}
}

func (a *App) connectForSync(ctx context.Context, opts SyncOptions) error {
	connectOpts := wa.ConnectOptions{
		AllowQR:         opts.AllowQR,
		OnQRCode:        opts.OnQRCode,
		PairPhoneNumber: opts.PairPhoneNumber,
		OnPairCode:      opts.OnPairCode,
		// Only detach for already-authenticated sync, not for auth
		// bootstrap (AllowQR / phone pairing) where caller
		// cancellation must bound the QR/pairing flow.
		DetachSocket: opts.AllowQR == false && opts.PairPhoneNumber == "",
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
	if chat.Server == types.NewsletterServer {
		return "newsletter"
	}
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
	if pm.Chat != types.StatusBroadcastJID {
		if err := a.db.UpsertChat(chatJID, chatKind(pm.Chat), chatName, pm.Timestamp); err != nil {
			return err
		}
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
			_ = a.db.UpsertGroupWithHierarchy(gi.JID.String(), gi.GroupName.Name, gi.OwnerJID.String(), gi.GroupCreated, gi.IsParent, gi.LinkedParentJID.String())
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

	if pm.Chat == types.StatusBroadcastJID {
		return a.db.UpsertStatusMessage(store.UpsertStatusMessageParams{
			MsgID:         pm.ID,
			Timestamp:     pm.Timestamp,
			FromMe:        pm.FromMe,
			SenderJID:     senderJID,
			SenderName:    senderName,
			Text:          pm.Text,
			MediaType:     mediaType,
			MediaCaption:  caption,
			Filename:      filename,
			MimeType:      mimeType,
			DirectPath:    directPath,
			MediaKey:      mediaKey,
			FileSHA256:    fileSha,
			FileEncSHA256: fileEncSha,
			FileLength:    fileLen,
		})
	}

	displayText := a.buildDisplayText(ctx, pm)
	if pm.Revoked {
		displayText = store.DeletedMessageDisplayText
	}

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
		QuotedMsgID:     pm.ReplyToID,
		QuotedSenderJID: pm.ReplyToSenderJID,
		Buttons:         waButtonsToStore(pm.Buttons),
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
		Edited:          pm.Edited,
		Revoked:         pm.Revoked,
	}); err != nil {
		return err
	}
	if pm.Call != nil {
		pm.Call.Chat = pm.Chat
		if pm.Call.SenderJID == "" {
			pm.Call.SenderJID = senderJID
		}
		if pm.Call.Timestamp.IsZero() {
			pm.Call.Timestamp = pm.Timestamp
		}
		if err := a.storeParsedCallEvent(ctx, *pm.Call, chatName, senderName); err != nil {
			return err
		}
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

func (a *App) storeParsedCallEvent(ctx context.Context, call wa.ParsedCallEvent, chatName, senderName string) error {
	call.Chat = a.canonicalStoreJID(ctx, call.Chat)
	chatJID := canonicalJIDString(call.Chat)
	if chatJID == "" {
		return fmt.Errorf("call chat JID is required")
	}
	if chatName == "" {
		chatName = a.wa.ResolveChatName(ctx, call.Chat, "")
	}
	if err := a.db.UpsertChat(chatJID, chatKind(call.Chat), chatName, call.Timestamp); err != nil {
		return err
	}

	senderJID := strings.TrimSpace(call.SenderJID)
	if senderJID != "" {
		if jid, err := types.ParseJID(senderJID); err == nil {
			contactJID := a.canonicalStoreJID(ctx, jid)
			senderJID = contactJID.String()
			if senderName == "" {
				if info, err := a.wa.GetContact(ctx, contactJID); err == nil {
					senderName = wa.BestContactName(info)
				}
			}
		}
	}

	participants := make([]store.CallParticipant, 0, len(call.Participants))
	for _, p := range call.Participants {
		jid := strings.TrimSpace(p.JID)
		if jid != "" {
			if parsed, err := types.ParseJID(jid); err == nil {
				jid = canonicalJIDString(a.canonicalStoreJID(ctx, parsed))
			}
		}
		if jid == "" {
			continue
		}
		participants = append(participants, store.CallParticipant{
			JID:     jid,
			Outcome: p.Outcome,
		})
	}

	return a.db.UpsertCallEvent(store.UpsertCallEventParams{
		ChatJID:      chatJID,
		ChatName:     chatName,
		SenderJID:    senderJID,
		SenderName:   senderName,
		CallID:       call.CallID,
		MsgID:        call.MsgID,
		EventType:    call.EventType,
		Direction:    call.Direction,
		Media:        call.Media,
		Outcome:      call.Outcome,
		Reason:       call.Reason,
		CallType:     call.CallType,
		DurationSecs: call.DurationSecs,
		Timestamp:    call.Timestamp,
		Participants: participants,
	})
}

func (a *App) deleteParsedCallEvents(ctx context.Context, deleted wa.ParsedCallDelete) error {
	chat := a.canonicalStoreJID(ctx, deleted.Chat)
	chatJID := canonicalJIDString(chat)
	if chatJID == "" {
		return fmt.Errorf("call chat JID is required")
	}
	_, err := a.db.DeleteCallEvents(store.DeleteCallEventsParams{
		ChatJID:   chatJID,
		Direction: deleted.Direction,
	})
	return err
}

func waButtonsToStore(buttons []wa.Button) []store.Button {
	if len(buttons) == 0 {
		return nil
	}
	out := make([]store.Button, len(buttons))
	for i, b := range buttons {
		out[i] = store.Button{
			Type:         b.Type,
			DisplayText:  b.DisplayText,
			ID:           b.ID,
			URL:          b.URL,
			PhoneNumber:  b.PhoneNumber,
			Description:  b.Description,
			ResponseType: b.ResponseType,
			Index:        b.Index,
		}
	}
	return out
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
	if pm.Call != nil {
		return callDisplayText(*pm.Call)
	}
	if pm.Media != nil {
		return "Sent " + mediaLabel(pm.Media.Type)
	}
	if text := strings.TrimSpace(pm.Text); text != "" {
		return text
	}
	return ""
}

func callDisplayText(call wa.ParsedCallEvent) string {
	parts := []string{"WhatsApp"}
	if call.Media != "" {
		parts = append(parts, call.Media)
	}
	parts = append(parts, "call")
	if call.Outcome != "" {
		parts = append(parts, call.Outcome)
	} else if call.EventType != "" && call.EventType != "call_log" {
		parts = append(parts, call.EventType)
	}
	if call.DurationSecs > 0 {
		parts = append(parts, fmt.Sprintf("(%s)", formatCallDuration(call.DurationSecs)))
	}
	return strings.Join(parts, " ")
}

func formatCallDuration(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	minutes := seconds / 60
	secs := seconds % 60
	if minutes <= 0 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dm%02ds", minutes, secs)
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
