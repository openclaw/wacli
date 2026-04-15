package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
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
	ExecCommand     string        // command to execute on new message
	WebhookURL      string        // URL to POST new message JSON
	WebhookSecret   string        // secret for HMAC-SHA256 X-Wacli-Signature header
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
		enqueueMedia = func(chatJID, msgID string) {
			if strings.TrimSpace(chatJID) == "" || strings.TrimSpace(msgID) == "" {
				return
			}
			select {
			case mediaJobs <- mediaJob{chatJID: chatJID, msgID: msgID}:
			default:
				// Avoid blocking the event handler.
				go func() {
					select {
					case mediaJobs <- mediaJob{chatJID: chatJID, msgID: msgID}:
					case <-ctx.Done():
					}
				}()
			}
		}
	}

	handlerID := a.wa.AddEventHandler(func(evt interface{}) {
		// Recover from panics so unexpected message structures do not
		// crash the entire process (#52).
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "\nevent handler panic (recovered): %v\n", r)
			}
		}()
		lastEvent.Store(time.Now().UTC().UnixNano())

		switch v := evt.(type) {
		case *events.Message:
			pm := wa.ParseLiveMessage(v)
			if pm.ReactionToID != "" && pm.ReactionEmoji == "" && v.Message != nil && v.Message.GetEncReactionMessage() != nil {
				if reaction, err := a.wa.DecryptReaction(ctx, v); err == nil && reaction != nil {
					pm.ReactionEmoji = reaction.GetText()
					if pm.ReactionToID == "" {
						if key := reaction.GetKey(); key != nil {
							pm.ReactionToID = key.GetID()
						}
					}
				}
			}
			if err := a.storeParsedMessage(ctx, pm); err == nil {
				messagesStored.Add(1)
				// Dispatch hooks via worker pool
				if a.hookChan != nil && (opts.ExecCommand != "" || opts.WebhookURL != "") {
					select {
					case a.hookChan <- parsedMessageJob{pm: pm, opts: opts}:
					default:
						fmt.Fprintln(os.Stderr, "\nWarning: Hook queue full, skipping message.")
					}
				}
			}
			if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
				enqueueMedia(pm.Chat.String(), pm.ID)
			}
			if messagesStored.Load()%25 == 0 {
				fmt.Fprintf(os.Stderr, "\rSynced %d messages...", messagesStored.Load())
			}
		case *events.HistorySync:
			fmt.Fprintf(os.Stderr, "\nProcessing history sync (%d conversations)...\n", len(v.Data.Conversations))
			for _, conv := range v.Data.Conversations {
				lastEvent.Store(time.Now().UTC().UnixNano())
				chatID := strings.TrimSpace(conv.GetID())
				if chatID == "" {
					continue
				}
				for _, m := range conv.Messages {
					lastEvent.Store(time.Now().UTC().UnixNano())
					if m.Message == nil {
						continue
					}
					pm := wa.ParseHistoryMessage(chatID, m.Message)
					if pm.ID == "" || pm.Chat.IsEmpty() {
						continue
					}
					if err := a.storeParsedMessage(ctx, pm); err == nil {
						messagesStored.Add(1)
					}
					if opts.DownloadMedia && pm.Media != nil && pm.ID != "" {
						enqueueMedia(pm.Chat.String(), pm.ID)
					}
				}
			}
			fmt.Fprintf(os.Stderr, "\rSynced %d messages...", messagesStored.Load())
		case *events.Connected:
			fmt.Fprintln(os.Stderr, "\nConnected.")
		case *events.Disconnected:
			fmt.Fprintln(os.Stderr, "\nDisconnected.")
			select {
			case disconnected <- struct{}{}:
			default:
			}
		}
	})
	defer a.wa.RemoveEventHandler(handlerID)

	if err := a.Connect(ctx, opts.AllowQR, opts.OnQRCode); err != nil {
		return SyncResult{}, err
	}

	if opts.DownloadMedia {
		var err error
		stopMedia, err = a.runMediaWorkers(ctx, mediaJobs, 4)
		if err != nil {
			return SyncResult{}, err
		}
		defer stopMedia()
	}

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
		for {
			select {
			case <-ctx.Done():
				fmt.Fprintln(os.Stderr, "\nStopping sync.")
				return SyncResult{MessagesStored: messagesStored.Load()}, nil
			case <-disconnected:
				fmt.Fprintln(os.Stderr, "Reconnecting...")
				if err := a.reconnect(ctx, opts.MaxReconnect); err != nil {
					return SyncResult{MessagesStored: messagesStored.Load()}, err
				}
			}
		}
	}

	// Bootstrap/once: exit after idle.
	poll := 250 * time.Millisecond
	if opts.IdleExit >= 2*time.Second {
		poll = 1 * time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nStopping sync.")
			return SyncResult{MessagesStored: messagesStored.Load()}, nil
		case <-disconnected:
			fmt.Fprintln(os.Stderr, "Reconnecting...")
			if err := a.reconnect(ctx, opts.MaxReconnect); err != nil {
				return SyncResult{MessagesStored: messagesStored.Load()}, err
			}
		case <-ticker.C:
			last := time.Unix(0, lastEvent.Load())
			if time.Since(last) >= opts.IdleExit {
				fmt.Fprintf(os.Stderr, "\nIdle for %s, exiting.\n", opts.IdleExit)
				return SyncResult{MessagesStored: messagesStored.Load()}, nil
			}
		}
	}
}

// reconnect wraps ReconnectWithBackoff with an optional deadline.
// If maxDuration is positive, reconnection gives up after that long.
// A zero or negative value means retry indefinitely (until ctx is cancelled).
func (a *App) reconnect(ctx context.Context, maxDuration time.Duration) error {
	rctx := ctx
	var cancel context.CancelFunc
	if maxDuration > 0 {
		rctx, cancel = context.WithTimeout(ctx, maxDuration)
		defer cancel()
	}
	err := a.wa.ReconnectWithBackoff(rctx, 2*time.Second, 30*time.Second)
	if err != nil && ctx.Err() == nil {
		// Deadline hit but parent context is still alive — we gave up, not the user.
		return fmt.Errorf("could not reconnect after %s: %w", maxDuration, err)
	}
	return err
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

func (a *App) dispatchHooks(ctx context.Context, opts SyncOptions, pm wa.ParsedMessage) {
	data, err := json.Marshal(pm)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nError marshaling message for hooks: %v\n", err)
		return
	}

	// Exec Hook
	if opts.ExecCommand != "" {
		cmd := exec.CommandContext(ctx, "sh", "-c", opts.ExecCommand)
		cmd.Stdin = bytes.NewReader(data)
		cmd.Env = append(os.Environ(), 
			"WACLI_MSG_ID="+pm.ID,
			"WACLI_CHAT_JID="+pm.Chat.String(),
			"WACLI_SENDER_JID="+pm.SenderJID,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "\nExec hook error: %v (output: %s)\n", err, string(out))
		}
	}

	// Webhook Hook
	if opts.WebhookURL != "" {
		httpClient := &http.Client{
			Timeout: 15 * time.Second,
		}
		req, err := http.NewRequestWithContext(ctx, "POST", opts.WebhookURL, bytes.NewReader(data))
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nWebhook request error: %v\n", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "wacli-bridge/"+a.Version())
		if opts.WebhookSecret != "" {
			mac := hmac.New(sha256.New, []byte(opts.WebhookSecret))
			mac.Write(data)
			req.Header.Set("X-Wacli-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nWebhook post error: %v\n", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			fmt.Fprintf(os.Stderr, "\nWebhook returned status: %s\n", resp.Status)
		}
	}
}
