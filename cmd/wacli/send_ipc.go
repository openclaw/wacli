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
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

const (
	sendDelegateVersion    = 1
	sendDelegateSocketName = ".send.sock"
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
	PTT                  bool     `json:"ptt,omitempty"`
	ID                   string   `json:"id,omitempty"`
	Reaction             string   `json:"reaction,omitempty"`
	Sender               string   `json:"sender,omitempty"`
	Question             string   `json:"question,omitempty"`
	Options              []string `json:"options,omitempty"`
	Selectable           int      `json:"selectable,omitempty"`
	PostSendWaitMS       int64    `json:"post_send_wait_ms,omitempty"`
	TimeoutMS            int64    `json:"timeout_ms,omitempty"`
}

type sendDelegateResponse struct {
	OK       bool              `json:"ok"`
	Error    string            `json:"error,omitempty"`
	Sent     bool              `json:"sent,omitempty"`
	To       string            `json:"to,omitempty"`
	ID       string            `json:"id,omitempty"`
	Target   string            `json:"target,omitempty"`
	Reaction string            `json:"reaction,omitempty"`
	Question string            `json:"question,omitempty"`
	Options  []string          `json:"options,omitempty"`
	Selected []string          `json:"selected,omitempty"`
	File     map[string]string `json:"file,omitempty"`
}

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

	_ = conn.SetDeadline(time.Now().Add(commandTimeout(flags)))
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

func startSendDelegateServer(ctx context.Context, a *app.App) (func(), error) {
	path := sendDelegateSocketPath(a.StoreDir())
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
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleSendDelegateConn(ctx, conn, a, &sendMu)
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

func handleSendDelegateConn(ctx context.Context, conn net.Conn, a *app.App, sendMu *sync.Mutex) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	var req sendDelegateRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(sendDelegateResponse{OK: false, Error: err.Error()})
		return
	}
	sendMu.Lock()
	defer sendMu.Unlock()

	resp, err := executeDelegatedSend(ctx, a, req)
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
	case "poll":
		return executeDelegatedPoll(ctx, a, req)
	case "poll_vote":
		return executeDelegatedPollVote(ctx, a, req)
	default:
		return sendDelegateResponse{}, fmt.Errorf("unsupported send kind %q", req.Kind)
	}
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
	chatName := a.WA().ResolveChatName(ctx, toJID, "")
	_ = a.DB().UpsertChat(toJID.String(), chatKindFromJID(toJID), chatName, now)
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    toJID.String(),
		ChatName:   chatName,
		MsgID:      string(msgID),
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       req.Message,
	})
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: string(msgID)}, nil
}

func executeDelegatedFile(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	res, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (sendDelegateResponse, error) {
		msgID, meta, err := sendFile(ctx, a, toJID, req.File, sendFileOptions{
			filename:      req.Filename,
			caption:       req.Caption,
			mimeOverride:  req.MIME,
			replyTo:       req.ReplyTo,
			replyToSender: req.ReplyToSender,
			ptt:           req.PTT || req.Kind == "voice",
		})
		if err != nil {
			return sendDelegateResponse{}, err
		}
		return sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: msgID, File: meta}, nil
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
		msgID, meta, err := sendSticker(ctx, a, toJID, req.File, sendStickerOptions{
			replyTo:       req.ReplyTo,
			replyToSender: req.ReplyToSender,
		})
		if err != nil {
			return sendDelegateResponse{}, err
		}
		return sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: msgID, File: meta}, nil
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
	upsertSentReaction(a.DB(), chat, chatName, sentID, req.ID, req.Reaction, now)
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return sendDelegateResponse{OK: true, Sent: true, To: chat.String(), ID: string(sentID), Target: req.ID, Reaction: req.Reaction}, nil
}

func writeDelegatedSendOutput(flags *rootFlags, kind string, resp sendDelegateResponse) error {
	if flags.asJSON {
		body := map[string]any{"sent": resp.Sent, "to": resp.To, "id": resp.ID}
		if resp.File != nil {
			body["file"] = resp.File
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
		return out.WriteJSON(os.Stdout, body)
	}
	switch kind {
	case "file":
		fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", resp.File["name"], resp.To, resp.ID)
	case "sticker":
		fmt.Fprintf(os.Stdout, "Sent sticker to %s (id %s)\n", resp.To, resp.ID)
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
