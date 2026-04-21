package app

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

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
	AfterConnect    func(context.Context) error
	DownloadMedia   bool
	RefreshContacts bool
	RefreshGroups   bool
	IdleExit        time.Duration // only used for bootstrap/once
	MaxReconnect    time.Duration // max time to attempt reconnection before giving up (0 = unlimited)
	Verbosity       int           // future
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

	if err := a.OpenWA(); err != nil {
		return SyncResult{}, err
	}

	var messagesStored atomic.Int64
	lastEvent := atomic.Int64{}
	lastEvent.Store(time.Now().UTC().UnixNano())

	disconnected := make(chan struct{}, 1)

	var stopMedia func()
	var mediaJobs chan mediaJob
	enqueueMedia := func(chatJID, msgID string) {}
	if opts.DownloadMedia {
		mediaJobs = make(chan mediaJob, 512)
		enqueueMedia = newMediaEnqueuer(ctx, mediaJobs)
	}

	handlerID := a.addSyncEventHandler(ctx, opts, &messagesStored, &lastEvent, disconnected, enqueueMedia)
	defer a.wa.RemoveEventHandler(handlerID)

	if opts.DownloadMedia {
		var err error
		stopMedia, err = a.runMediaWorkers(ctx, mediaJobs, 4)
		if err != nil {
			return SyncResult{}, err
		}
		defer stopMedia()
	}

	if err := a.Connect(ctx, opts.AllowQR, opts.OnQRCode); err != nil {
		return SyncResult{}, err
	}
	lastEvent.Store(time.Now().UTC().UnixNano())

	// Optional: bootstrap imports (helps contacts/groups management without waiting for events).
	if opts.RefreshContacts {
		_ = a.refreshContacts(ctx)
	}
	if opts.RefreshGroups {
		_ = a.refreshGroups(ctx)
	}
	if opts.AfterConnect != nil {
		if err := opts.AfterConnect(ctx); err != nil {
			return SyncResult{MessagesStored: messagesStored.Load()}, err
		}
	}

	if opts.Mode == SyncModeFollow {
		return a.runSyncFollow(ctx, opts.MaxReconnect, &messagesStored, disconnected)
	}

	return a.runSyncUntilIdle(ctx, opts.IdleExit, opts.MaxReconnect, &messagesStored, &lastEvent, disconnected)
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
	chatJID := pm.Chat.String()
	chatName := a.wa.ResolveChatName(ctx, pm.Chat, pm.PushName)
	if err := a.db.UpsertChat(chatJID, chatKind(pm.Chat), chatName, pm.Timestamp); err != nil {
		return err
	}

	// Best-effort: store contact info for DMs.
	if pm.Chat.Server == types.DefaultUserServer {
		if info, err := a.wa.GetContact(ctx, pm.Chat.ToNonAD()); err == nil {
			_ = a.db.UpsertContact(
				pm.Chat.String(),
				pm.Chat.User,
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
	if pm.SenderJID != "" {
		if jid, err := types.ParseJID(pm.SenderJID); err == nil {
			if info, err := a.wa.GetContact(ctx, jid.ToNonAD()); err == nil {
				if name := wa.BestContactName(info); name != "" {
					senderName = name
				}
				_ = a.db.UpsertContact(
					jid.String(),
					jid.User,
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
					UserJID:  p.JID.String(),
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

	return a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       chatJID,
		ChatName:      chatName,
		MsgID:         pm.ID,
		SenderJID:     pm.SenderJID,
		SenderName:    senderName,
		Timestamp:     pm.Timestamp,
		FromMe:        pm.FromMe,
		Text:          pm.Text,
		DisplayText:   displayText,
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
