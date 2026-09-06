package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

const (
	sendDelegateVersion       = 1
	sendDelegateSocketName    = ".send.sock"
	sendDelegateResponseGrace = 5 * time.Second
)

var errSendDelegateUnavailable = errors.New("send delegate unavailable")

type sendDelegateRequest struct {
	Version              int      `json:"version"`
	Kind                 string   `json:"kind"`
	To                   string   `json:"to,omitempty"`
	Pick                 int      `json:"pick,omitempty"`
	Message              string   `json:"message,omitempty"`
	Mentions             []string `json:"mentions,omitempty"`
	ReplyTo              string   `json:"reply_to,omitempty"`
	ReplyToSender        string   `json:"reply_to_sender,omitempty"`
	NoPreview            bool     `json:"no_preview,omitempty"`
	Ephemeral            bool     `json:"ephemeral,omitempty"`
	EphemeralDuration    string   `json:"ephemeral_duration,omitempty"`
	EphemeralDurationSet bool     `json:"ephemeral_duration_set,omitempty"`
	File                 string   `json:"file,omitempty"`
	Filename             string   `json:"filename,omitempty"`
	Caption              string   `json:"caption,omitempty"`
	MIME                 string   `json:"mime,omitempty"`
	As                   string   `json:"as,omitempty"`
	PTT                  bool     `json:"ptt,omitempty"`
	ID                   string   `json:"id,omitempty"`
	Reaction             string   `json:"reaction,omitempty"`
	Sender               string   `json:"sender,omitempty"`
	Label                string   `json:"label,omitempty"`
	ButtonID             string   `json:"button_id,omitempty"`
	SelectIndex          int      `json:"select_index,omitempty"`
	Type                 string   `json:"type,omitempty"`
	Latitude             float64  `json:"latitude,omitempty"`
	Longitude            float64  `json:"longitude,omitempty"`
	Name                 string   `json:"name,omitempty"`
	Question             string   `json:"question,omitempty"`
	Options              []string `json:"options,omitempty"`
	Selectable           int      `json:"selectable,omitempty"`
	PresenceState        string   `json:"presence_state,omitempty"`
	PresenceMedia        string   `json:"presence_media,omitempty"`
	Read                 *bool    `json:"read,omitempty"`
	PostSendWaitMS       int64    `json:"post_send_wait_ms,omitempty"`
	TimeoutMS            int64    `json:"timeout_ms,omitempty"`
	DeadlineUnixMS       int64    `json:"deadline_unix_ms,omitempty"`
}

type sendDelegateResponse struct {
	OK             bool              `json:"ok"`
	Error          string            `json:"error,omitempty"`
	Sent           bool              `json:"sent,omitempty"`
	To             string            `json:"to,omitempty"`
	ID             string            `json:"id,omitempty"`
	Target         string            `json:"target,omitempty"`
	Reaction       string            `json:"reaction,omitempty"`
	Question       string            `json:"question,omitempty"`
	Options        []string          `json:"options,omitempty"`
	Selected       []string          `json:"selected,omitempty"`
	SelectedOption *selectOption     `json:"selected_option,omitempty"`
	File           map[string]string `json:"file,omitempty"`
	StoreWarning   string            `json:"store_warning,omitempty"`
	Chat           string            `json:"chat,omitempty"`
	Action         string            `json:"action,omitempty"`
}

type sendDelegateExecutor func(context.Context, sendDelegateRequest) (sendDelegateResponse, error)

func sendDelegateSocketPath(storeDir string) string {
	return filepath.Join(storeDir, sendDelegateSocketName)
}

func delegateSend(ctx context.Context, flags *rootFlags, req sendDelegateRequest) (sendDelegateResponse, error) {
	req.Version = sendDelegateVersion
	req.TimeoutMS = durationMillis(flags.timeout)
	storeDir, err := resolveStoreDir(flags)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	path := sendDelegateSocketPath(storeDir)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return sendDelegateResponse{}, fmt.Errorf("%w: %v", errSendDelegateUnavailable, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(commandTimeout(flags))
	req.DeadlineUnixMS = deadline.UnixMilli()
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return sendDelegateResponse{}, err
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return sendDelegateResponse{}, err
	}
	if !resp.OK {
		return sendDelegateResponse{}, errors.New(resp.Error)
	}
	return resp, nil
}

func tryDelegateSend(ctx context.Context, flags *rootFlags, lockErr error, req sendDelegateRequest) (sendDelegateResponse, bool, error) {
	if !lock.IsLocked(lockErr) {
		return sendDelegateResponse{}, false, lockErr
	}
	resp, err := delegateSend(ctx, flags, req)
	if err != nil {
		if errors.Is(err, errSendDelegateUnavailable) {
			return sendDelegateResponse{}, false, lockErr
		}
		return sendDelegateResponse{}, true, err
	}
	return resp, true, nil
}

func startSendDelegateServer(ctx context.Context, a *app.App, spacing sendSpacing) (func(), error) {
	return startSendDelegateServerForStore(ctx, a.StoreDir(), spacing, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeDelegatedSend(ctx, a, req)
	})
}

func startSendDelegateServerForStore(ctx context.Context, storeDir string, spacing sendSpacing, execute sendDelegateExecutor) (func(), error) {
	path := sendDelegateSocketPath(storeDir)
	if err := removeStaleSendDelegateSocket(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, err
	}

	done := make(chan struct{})
	var sendMu sync.Mutex
	var pacedSendSlot chan struct{}
	if spacing.enabled() {
		pacedSendSlot = make(chan struct{}, 1)
		pacedSendSlot <- struct{}{}
	}
	// One pacer shared across connections: it spaces the serialized delegated
	// sends so a burst of `wacli send` processes delegating to this daemon
	// leaves the wire paced instead of back-to-back. Disabled = no-op.
	pacer := newSendPacer(spacing)
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSendDelegateConn(ctx, conn, execute, &sendMu, pacedSendSlot, pacer)
		}
	}()

	stop := func() {
		_ = ln.Close()
		<-done
		_ = os.Remove(path)
	}
	return stop, nil
}

func removeStaleSendDelegateSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	return os.Remove(path)
}

func handleSendDelegateConn(ctx context.Context, conn net.Conn, execute sendDelegateExecutor, sendMu *sync.Mutex, pacedSendSlot chan struct{}, pacer *sendPacer) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	var req sendDelegateRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(sendDelegateResponse{OK: false, Error: err.Error()})
		return
	}
	requestCtx := ctx
	if pacer.enabled() {
		deadline := time.Now().Add(millisDuration(req.TimeoutMS, 5*time.Minute))
		if req.DeadlineUnixMS > 0 {
			callerDeadline := time.UnixMilli(req.DeadlineUnixMS)
			if callerDeadline.Before(deadline) {
				deadline = callerDeadline
			}
		}
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
		if requestDeadline, ok := requestCtx.Deadline(); ok {
			// The fixed initial deadline only protects request decoding. A paced
			// request may intentionally run longer than five minutes, so keep the
			// transport alive through its budget and the final response write.
			_ = conn.SetDeadline(requestDeadline.Add(sendDelegateResponseGrace))
		}
	}

	if pacer.enabled() {
		select {
		case <-requestCtx.Done():
			_ = json.NewEncoder(conn).Encode(sendDelegateResponse{
				OK:    false,
				Error: "send spacing exceeded request timeout before dispatch",
			})
			return
		case <-pacedSendSlot:
			defer func() { pacedSendSlot <- struct{}{} }()
		}
	} else {
		// Preserve the original unpaced serialization path exactly when the
		// opt-in flag is unset.
		sendMu.Lock()
		defer sendMu.Unlock()
	}

	// Space this send from the previous one while serialized. Bound the wait by
	// the caller's request timeout, including time spent waiting for earlier
	// delegated sends, and have pacing + send share that one deadline. Disabled
	// spacing leaves the path untouched.
	if pacer.enabled() {
		if !pacer.wait(requestCtx) {
			_ = json.NewEncoder(conn).Encode(sendDelegateResponse{
				OK:    false,
				Error: "send spacing exceeded request timeout before dispatch",
			})
			return
		}
	}

	resp, err := execute(requestCtx, req)
	if pacer.enabled() {
		// Record completion, not handler entry: recipient resolution, media
		// preparation, and the actual wire send all happen inside execute.
		// Starting the gap here prevents a slow operation from consuming it.
		pacer.record()
	}
	if err != nil {
		resp = sendDelegateResponse{OK: false, Error: err.Error()}
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

func executeDelegatedSend(parent context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.Version != sendDelegateVersion {
		return sendDelegateResponse{}, fmt.Errorf("unsupported send delegate version %d", req.Version)
	}
	ctx, cancel := context.WithTimeout(parent, millisDuration(req.TimeoutMS, 5*time.Minute))
	defer cancel()

	switch req.Kind {
	case "text":
		return executeDelegatedText(ctx, a, req)
	case "file", "voice":
		return executeDelegatedFile(ctx, a, req)
	case "sticker":
		return executeDelegatedSticker(ctx, a, req)
	case "react":
		return executeDelegatedReact(ctx, a, req)
	case "location":
		return executeDelegatedLocation(ctx, a, req)
	case "poll":
		return executeDelegatedPoll(ctx, a, req)
	case "poll_vote":
		return executeDelegatedPollVote(ctx, a, req)
	case "button_list_select":
		return executeDelegatedButtonListSelect(ctx, a, req)
	case "presence":
		return executeDelegatedPresence(ctx, a, req)
	case "edit":
		return executeDelegatedEdit(ctx, a, req)
	case "mark_read":
		return executeDelegatedMarkRead(ctx, a, req)
	default:
		return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", req.Kind)
	}
}

type delegatedMarkReadApp interface {
	recipientResolverApp
	MarkChatRead(context.Context, types.JID, bool) error
}

func executeDelegatedMarkRead(ctx context.Context, a delegatedMarkReadApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	read := true
	if req.Read != nil {
		read = *req.Read
	}
	if err := a.MarkChatRead(ctx, toJID, read); err != nil {
		return sendDelegateResponse{}, err
	}
	action := "mark-read"
	if !read {
		action = "mark-unread"
	}
	return sendDelegateResponse{OK: true, Chat: toJID.String(), Action: action}, nil
}

func executeDelegatedPresence(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	state, err := presenceStateFromString(req.PresenceState)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID, err := wa.ParseUserOrJID(req.To)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	chatMedia, err := presenceMediaFromString(req.PresenceMedia)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if err := sendPresenceWithRetry(ctx, reconnectForSend(a), func(ctx context.Context) error {
		return a.WA().SendChatPresence(ctx, toJID, state, chatMedia)
	}); err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, Sent: true, To: toJID.String()}, nil
}

func executeDelegatedEdit(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	msg, chatJID, err := loadMessageMutationTarget(ctx, a, req.To, req.ID)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if err := validateMessageCanEdit(msg, time.Now().UTC()); err != nil {
		return sendDelegateResponse{}, err
	}
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return a.WA().EditMessage(ctx, chatJID, types.MessageID(msg.MsgID), req.Message)
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if err := a.DB().UpdateMessageText(msg.ChatJID, msg.MsgID, req.Message); err != nil {
		return sendDelegateResponse{}, fmt.Errorf("store edited message text: %w", err)
	}
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return sendDelegateResponse{OK: true, Sent: true, To: chatJID.String(), ID: string(sentID), Target: msg.MsgID}, nil
}

func executeDelegatedText(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	ephemeral := textEphemeralOptions{
		Enabled:     req.Ephemeral,
		Duration:    req.EphemeralDuration,
		DurationSet: req.EphemeralDurationSet,
	}
	if err := validateTextEphemeralOptions(ephemeral); err != nil {
		return sendDelegateResponse{}, err
	}
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if err := validateTextRecipient(a.WA(), toJID); err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	mentionedJIDs, err := parseMentionedJIDs(req.Mentions)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	preview := fetchLinkPreview(ctx, req.Message, req.NoPreview)
	msgID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return sendTextMessage(ctx, a, toJID, req.Message, req.ReplyTo, req.ReplyToSender, preview, mentionedJIDs, ephemeral)
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	now := time.Now().UTC()
	storeErr := persistOutboundText(ctx, a, toJID, string(msgID), req.Message, now)
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	resp := sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: string(msgID)}
	if storeErr != nil {
		resp.StoreWarning = storeErr.Error()
	}
	return resp, nil
}

func executeDelegatedFile(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	mediaAs, err := validateSendFileMediaOptions(req.As, req.PTT || req.Kind == "voice")
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	res, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (sendDelegateResponse, error) {
		outcome, err := sendFile(ctx, a, toJID, req.File, sendFileOptions{
			filename:      req.Filename,
			caption:       req.Caption,
			mimeOverride:  req.MIME,
			mediaAs:       mediaAs,
			replyTo:       req.ReplyTo,
			replyToSender: req.ReplyToSender,
			ptt:           req.PTT || req.Kind == "voice",
		})
		if err != nil {
			return sendDelegateResponse{}, err
		}
		resp := sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: outcome.id, File: outcome.meta}
		if outcome.storeWarning != nil {
			resp.StoreWarning = outcome.storeWarning.Error()
		}
		return resp, nil
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return res, nil
}

func executeDelegatedSticker(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	res, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (sendDelegateResponse, error) {
		outcome, err := sendSticker(ctx, a, toJID, req.File, sendStickerOptions{
			replyTo:       req.ReplyTo,
			replyToSender: req.ReplyToSender,
		})
		if err != nil {
			return sendDelegateResponse{}, err
		}
		resp := sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: outcome.id, File: outcome.meta}
		if outcome.storeWarning != nil {
			resp.StoreWarning = outcome.storeWarning.Error()
		}
		return resp, nil
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return res, nil
}

func executeDelegatedReact(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	chat, senderJID, err := reactionTarget(req.To, req.Sender)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	chat = warmupDelegatedRecipient(ctx, a, chat)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return a.WA().SendReaction(ctx, chat, senderJID, types.MessageID(req.ID), req.Reaction)
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	now := time.Now().UTC()
	chatName := a.WA().ResolveChatName(ctx, chat, "")
	storeErr := upsertSentReaction(a.DB(), chat, chatName, sentID, req.ID, req.Reaction, now)
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	resp := sendDelegateResponse{OK: true, Sent: true, To: chat.String(), ID: string(sentID), Target: req.ID, Reaction: req.Reaction}
	if storeErr != nil {
		resp.StoreWarning = storeErr.Error()
	}
	return resp, nil
}

func writeDelegatedSendOutput(flags *rootFlags, kind string, resp sendDelegateResponse) error {
	warnSendStoreFailureMsg(os.Stderr, resp.ID, resp.StoreWarning)
	if flags.asJSON {
		body := map[string]any{"sent": resp.Sent, "to": resp.To, "id": resp.ID}
		if resp.File != nil {
			body["file"] = resp.File
		}
		if resp.StoreWarning != "" {
			body["store_warning"] = resp.StoreWarning
		}
		if kind == "react" {
			body["target"] = resp.Target
			body["reaction"] = resp.Reaction
		}
		if kind == "poll" {
			body["question"] = resp.Question
			body["options"] = resp.Options
		}
		if kind == "poll_vote" {
			body["target"] = resp.Target
			body["selected"] = resp.Selected
		}
		if kind == "button_list_select" {
			body["target"] = resp.Target
			body["selected"] = resp.SelectedOption
		}
		return out.WriteJSON(os.Stdout, body)
	}
	switch kind {
	case "file":
		fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", resp.File["name"], resp.To, resp.ID)
	case "sticker":
		fmt.Fprintf(os.Stdout, "Sent sticker to %s (id %s)\n", resp.To, resp.ID)
	case "location":
		fmt.Fprintf(os.Stdout, "Sent location to %s (id %s)\n", resp.To, resp.ID)
	case "voice":
		fmt.Fprintf(os.Stdout, "Sent voice note to %s (id %s)\n", resp.To, resp.ID)
	case "react":
		if resp.Reaction == "" {
			fmt.Fprintf(os.Stdout, "Removed reaction from %s (id %s)\n", resp.Target, resp.ID)
		} else {
			fmt.Fprintf(os.Stdout, "Reacted %s to %s (id %s)\n", resp.Reaction, resp.Target, resp.ID)
		}
	case "poll":
		fmt.Fprintf(os.Stdout, "Sent poll to %s (id %s)\n", resp.To, resp.ID)
	case "poll_vote":
		fmt.Fprintf(os.Stdout, "Voted on %s in %s (id %s)\n", resp.Target, resp.To, resp.ID)
	case "button_list_select":
		label := ""
		if resp.SelectedOption != nil {
			label = resp.SelectedOption.DisplayText
		}
		fmt.Fprintf(os.Stdout, "Selected %q on %s in %s (id %s)\n", label, resp.Target, resp.To, resp.ID)
	default:
		fmt.Fprintf(os.Stdout, "Sent to %s (id %s)\n", resp.To, resp.ID)
	}
	return nil
}

func warmupDelegatedRecipient(ctx context.Context, a *app.App, jid types.JID) types.JID {
	return warmupRecipient(ctx, a.WA(), jid, os.Stderr)
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d / time.Millisecond)
}

func millisDuration(ms int64, fallback time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func commandTimeout(flags *rootFlags) time.Duration {
	if flags == nil || flags.timeout <= 0 {
		return 5 * time.Minute
	}
	return flags.timeout
}
