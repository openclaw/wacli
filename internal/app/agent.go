package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// AgentOptions configures the agent loop.
type AgentOptions struct {
	AutoPresence bool
	TypingDelay  bool
}

// RunAgent starts the JSON-RPC 2.0 agent loop, reading requests from r and
// writing responses/notifications to w. It blocks until ctx is cancelled or
// stdin reaches EOF.
func (a *App) RunAgent(ctx context.Context, r io.Reader, w io.Writer, opts AgentOptions) error {
	if err := a.OpenWA(); err != nil {
		return err
	}
	if !a.wa.IsAuthed() {
		return fmt.Errorf("not authenticated; run `wacli auth`")
	}

	writer := newRPCWriter(w)

	// Register event handler before connecting.
	handlerID := a.wa.AddEventHandler(func(evt interface{}) {
		a.handleAgentEvent(ctx, writer, evt)
	})
	defer a.wa.RemoveEventHandler(handlerID)

	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}

	// Start offline and force delivery receipts.
	_ = a.wa.SendPresence(ctx, types.PresenceUnavailable)
	a.wa.SetForceActiveDeliveryReceipts(true)

	writer.notify("agent.ready", map[string]any{
		"version": a.opts.Version,
	})

	// Read stdin line-by-line (NDJSON) for robust error recovery.
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writer.respondError(nil, errCodeParse, "parse error: "+err.Error())
			continue
		}
		if req.JSONRPC != "2.0" {
			writer.respondError(req.ID, errCodeParse, "invalid jsonrpc version")
			continue
		}
		a.dispatchRPC(ctx, writer, req, opts)
	}
	return nil
}

func (a *App) dispatchRPC(ctx context.Context, w *rpcWriter, req rpcRequest, opts AgentOptions) {
	switch req.Method {
	case "send_text":
		a.rpcSendText(ctx, w, req, opts)
	case "send_file":
		a.rpcSendFile(ctx, w, req, opts)
	case "mark_read":
		a.rpcMarkRead(ctx, w, req)
	case "set_typing":
		a.rpcSetTyping(ctx, w, req)
	case "set_presence":
		a.rpcSetPresence(ctx, w, req)
	case "list_chats":
		a.rpcListChats(ctx, w, req)
	case "list_messages":
		a.rpcListMessages(ctx, w, req)
	case "search":
		a.rpcSearch(ctx, w, req)
	case "get_message":
		a.rpcGetMessage(ctx, w, req)
	default:
		w.respondError(req.ID, errCodeMethodNotFound, "method not found: "+req.Method)
	}
}

// --- RPC handlers ---

func (a *App) rpcSendText(ctx context.Context, w *rpcWriter, req rpcRequest, opts AgentOptions) {
	var p struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.To == "" || p.Text == "" {
		w.respondError(req.ID, errCodeInvalidParams, "to and text are required")
		return
	}
	toJID, err := wa.ParseUserOrJID(p.To)
	if err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid to: "+err.Error())
		return
	}

	if opts.AutoPresence {
		a.simulateTyping(ctx, toJID, p.Text)
	}

	msgID, err := a.wa.SendText(ctx, toJID, p.Text)
	if err != nil {
		w.respondError(req.ID, errCodeSendFailed, "send failed: "+err.Error())
		return
	}

	if opts.AutoPresence {
		_ = a.wa.SendPresence(ctx, types.PresenceUnavailable)
	}

	// Persist sent message locally.
	now := time.Now().UTC()
	chatName := a.wa.ResolveChatName(ctx, toJID, "")
	_ = a.db.UpsertChat(toJID.String(), chatKind(toJID), chatName, now)
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:    toJID.String(),
		ChatName:   chatName,
		MsgID:      string(msgID),
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       p.Text,
	})

	w.respond(req.ID, map[string]any{"message_id": msgID})
}

func (a *App) rpcSendFile(ctx context.Context, w *rpcWriter, req rpcRequest, opts AgentOptions) {
	var p struct {
		To       string `json:"to"`
		File     string `json:"file"`
		Filename string `json:"filename"`
		Caption  string `json:"caption"`
		Mime     string `json:"mime"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.To == "" || p.File == "" {
		w.respondError(req.ID, errCodeInvalidParams, "to and file are required")
		return
	}
	toJID, err := wa.ParseUserOrJID(p.To)
	if err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid to: "+err.Error())
		return
	}

	data, err := os.ReadFile(p.File)
	if err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "read file: "+err.Error())
		return
	}

	name := strings.TrimSpace(p.Filename)
	if name == "" {
		name = filepath.Base(p.File)
	}
	mimeType := strings.TrimSpace(p.Mime)
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(p.File)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}

	mediaType := "document"
	uploadType, _ := wa.MediaTypeFromString("document")
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = "image"
		uploadType, _ = wa.MediaTypeFromString("image")
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = "video"
		uploadType, _ = wa.MediaTypeFromString("video")
	case strings.HasPrefix(mimeType, "audio/"):
		mediaType = "audio"
		uploadType, _ = wa.MediaTypeFromString("audio")
	}

	if opts.AutoPresence {
		a.simulateTyping(ctx, toJID, "file")
	}

	up, err := a.wa.Upload(ctx, data, uploadType)
	if err != nil {
		w.respondError(req.ID, errCodeSendFailed, "upload failed: "+err.Error())
		return
	}

	msg := buildMediaProto(mediaType, mimeType, name, p.Caption, up)
	msgID, err := a.wa.SendProtoMessage(ctx, toJID, msg)
	if err != nil {
		w.respondError(req.ID, errCodeSendFailed, "send failed: "+err.Error())
		return
	}

	if opts.AutoPresence {
		_ = a.wa.SendPresence(ctx, types.PresenceUnavailable)
	}

	now := time.Now().UTC()
	chatName := a.wa.ResolveChatName(ctx, toJID, "")
	_ = a.db.UpsertChat(toJID.String(), chatKind(toJID), chatName, now)
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       toJID.String(),
		ChatName:      chatName,
		MsgID:         msgID,
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		Text:          p.Caption,
		MediaType:     mediaType,
		MediaCaption:  p.Caption,
		Filename:      name,
		MimeType:      mimeType,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	w.respond(req.ID, map[string]any{"message_id": msgID, "media_type": mediaType})
}

func (a *App) rpcMarkRead(ctx context.Context, w *rpcWriter, req rpcRequest) {
	var p struct {
		Chat       string   `json:"chat"`
		MessageIDs []string `json:"message_ids"`
		Sender     string   `json:"sender"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.Chat == "" || len(p.MessageIDs) == 0 {
		w.respondError(req.ID, errCodeInvalidParams, "chat and message_ids are required")
		return
	}
	chatJID, err := wa.ParseUserOrJID(p.Chat)
	if err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid chat: "+err.Error())
		return
	}
	ids := make([]types.MessageID, len(p.MessageIDs))
	for i, id := range p.MessageIDs {
		ids[i] = types.MessageID(id)
	}
	var senderJID types.JID
	if p.Sender != "" {
		senderJID, err = wa.ParseUserOrJID(p.Sender)
		if err != nil {
			w.respondError(req.ID, errCodeInvalidParams, "invalid sender: "+err.Error())
			return
		}
	}
	if err := a.wa.MarkRead(ctx, ids, time.Now(), chatJID, senderJID); err != nil {
		w.respondError(req.ID, errCodeSendFailed, "mark read failed: "+err.Error())
		return
	}
	w.respond(req.ID, map[string]any{})
}

func (a *App) rpcSetTyping(ctx context.Context, w *rpcWriter, req rpcRequest) {
	var p struct {
		Chat  string `json:"chat"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.Chat == "" || p.State == "" {
		w.respondError(req.ID, errCodeInvalidParams, "chat and state are required")
		return
	}
	chatJID, err := wa.ParseUserOrJID(p.Chat)
	if err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid chat: "+err.Error())
		return
	}
	var state types.ChatPresence
	switch p.State {
	case "composing":
		state = types.ChatPresenceComposing
	case "paused":
		state = types.ChatPresencePaused
	default:
		w.respondError(req.ID, errCodeInvalidParams, "state must be composing or paused")
		return
	}
	if err := a.wa.SendChatPresence(ctx, chatJID, state, ""); err != nil {
		w.respondError(req.ID, errCodeSendFailed, "set typing failed: "+err.Error())
		return
	}
	w.respond(req.ID, map[string]any{})
}

func (a *App) rpcSetPresence(ctx context.Context, w *rpcWriter, req rpcRequest) {
	var p struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	var state types.Presence
	switch p.State {
	case "available":
		state = types.PresenceAvailable
	case "unavailable":
		state = types.PresenceUnavailable
	default:
		w.respondError(req.ID, errCodeInvalidParams, "state must be available or unavailable")
		return
	}
	if err := a.wa.SendPresence(ctx, state); err != nil {
		w.respondError(req.ID, errCodeSendFailed, "set presence failed: "+err.Error())
		return
	}
	w.respond(req.ID, map[string]any{})
}

func (a *App) rpcListChats(ctx context.Context, w *rpcWriter, req rpcRequest) {
	var p struct {
		Limit int    `json:"limit"`
		Query string `json:"query"`
	}
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	chats, err := a.db.ListChats(p.Query, p.Limit)
	if err != nil {
		w.respondError(req.ID, errCodeSendFailed, "list chats: "+err.Error())
		return
	}
	out := make([]map[string]any, len(chats))
	for i, c := range chats {
		out[i] = map[string]any{
			"jid":             c.JID,
			"name":            c.Name,
			"kind":            c.Kind,
			"last_message_ts": c.LastMessageTS.Unix(),
		}
	}
	w.respond(req.ID, out)
}

func (a *App) rpcListMessages(ctx context.Context, w *rpcWriter, req rpcRequest) {
	var p struct {
		Chat   string `json:"chat"`
		Limit  int    `json:"limit"`
		Before *int64 `json:"before"`
		After  *int64 `json:"after"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.Chat == "" {
		w.respondError(req.ID, errCodeInvalidParams, "chat is required")
		return
	}
	params := store.ListMessagesParams{
		ChatJID: p.Chat,
		Limit:   p.Limit,
	}
	if p.Before != nil {
		t := time.Unix(*p.Before, 0).UTC()
		params.Before = &t
	}
	if p.After != nil {
		t := time.Unix(*p.After, 0).UTC()
		params.After = &t
	}
	msgs, err := a.db.ListMessages(params)
	if err != nil {
		w.respondError(req.ID, errCodeSendFailed, "list messages: "+err.Error())
		return
	}
	w.respond(req.ID, messagesToJSON(msgs))
}

func (a *App) rpcSearch(ctx context.Context, w *rpcWriter, req rpcRequest) {
	var p struct {
		Query string `json:"query"`
		Chat  string `json:"chat"`
		From  string `json:"from"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.Query == "" {
		w.respondError(req.ID, errCodeInvalidParams, "query is required")
		return
	}
	msgs, err := a.db.SearchMessages(store.SearchMessagesParams{
		Query:   p.Query,
		ChatJID: p.Chat,
		From:    p.From,
		Limit:   p.Limit,
	})
	if err != nil {
		w.respondError(req.ID, errCodeSendFailed, "search: "+err.Error())
		return
	}
	w.respond(req.ID, messagesToJSON(msgs))
}

func (a *App) rpcGetMessage(ctx context.Context, w *rpcWriter, req rpcRequest) {
	var p struct {
		Chat string `json:"chat"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		w.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
		return
	}
	if p.Chat == "" || p.ID == "" {
		w.respondError(req.ID, errCodeInvalidParams, "chat and id are required")
		return
	}
	msg, err := a.db.GetMessage(p.Chat, p.ID)
	if err != nil {
		if store.IsNotFound(err) {
			w.respondError(req.ID, errCodeInvalidParams, "message not found")
		} else {
			w.respondError(req.ID, errCodeSendFailed, "get message: "+err.Error())
		}
		return
	}
	w.respond(req.ID, messageToJSON(msg))
}

// --- Event handler ---

func (a *App) handleAgentEvent(ctx context.Context, w *rpcWriter, evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		pm := wa.ParseLiveMessage(v)
		if sm, err := a.storeParsedMessage(ctx, pm); err == nil {
			w.notify("message", streamMessageToJSON(sm))
		}
	case *events.HistorySync:
		for _, conv := range v.Data.Conversations {
			chatID := strings.TrimSpace(conv.GetID())
			if chatID == "" {
				continue
			}
			for _, m := range conv.Messages {
				if m.Message == nil {
					continue
				}
				pm := wa.ParseHistoryMessage(chatID, m.Message)
				if pm.ID == "" || pm.Chat.IsEmpty() {
					continue
				}
				if sm, err := a.storeParsedMessage(ctx, pm); err == nil {
					w.notify("message", streamMessageToJSON(sm))
				}
			}
		}
	case *events.Receipt:
		w.notify("receipt", map[string]any{
			"chat_jid":    v.Chat.String(),
			"sender_jid":  v.Sender.String(),
			"message_ids": v.MessageIDs,
			"type":        string(v.Type),
			"timestamp":   v.Timestamp.Unix(),
		})
	case *events.Presence:
		w.notify("presence", map[string]any{
			"from_jid":    v.From.String(),
			"unavailable": v.Unavailable,
			"last_seen":   v.LastSeen.Unix(),
		})
	case *events.ChatPresence:
		w.notify("chat_presence", map[string]any{
			"chat_jid":   v.Chat.String(),
			"sender_jid": v.Sender.String(),
			"state":      string(v.State),
			"media":      string(v.Media),
		})
	case *events.Connected:
		w.notify("connection", map[string]any{"state": "connected"})
	case *events.Disconnected:
		w.notify("connection", map[string]any{"state": "disconnected"})
	}
}

// --- Typing simulation ---

func (a *App) simulateTyping(ctx context.Context, chat types.JID, text string) {
	_ = a.wa.SendPresence(ctx, types.PresenceAvailable)
	_ = a.wa.SendChatPresence(ctx, chat, types.ChatPresenceComposing, "")

	delay := time.Duration(len(text)) * 30 * time.Millisecond
	if delay < 500*time.Millisecond {
		delay = 500 * time.Millisecond
	}
	if delay > 3*time.Second {
		delay = 3 * time.Second
	}

	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}

	_ = a.wa.SendChatPresence(ctx, chat, types.ChatPresencePaused, "")
}

// --- Helpers ---

func messagesToJSON(msgs []store.Message) []map[string]any {
	out := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		out[i] = messageToJSON(m)
	}
	return out
}

func messageToJSON(m store.Message) map[string]any {
	return map[string]any{
		"chat_jid":     m.ChatJID,
		"chat_name":    m.ChatName,
		"msg_id":       m.MsgID,
		"sender_jid":   m.SenderJID,
		"timestamp":    m.Timestamp.Unix(),
		"from_me":      m.FromMe,
		"text":         m.Text,
		"display_text": m.DisplayText,
		"media_type":   m.MediaType,
	}
}

func streamMessageToJSON(sm StreamMessage) map[string]any {
	return map[string]any{
		"chat_jid":     sm.ChatJID,
		"chat_name":    sm.ChatName,
		"msg_id":       sm.MsgID,
		"sender_jid":   sm.SenderJID,
		"sender_name":  sm.SenderName,
		"timestamp":    sm.Timestamp,
		"from_me":      sm.FromMe,
		"text":         sm.Text,
		"display_text": sm.DisplayText,
		"media_type":   sm.MediaType,
	}
}

func buildMediaProto(mediaType, mimeType, name, caption string, up whatsmeow.UploadResponse) *waProto.Message {
	msg := &waProto.Message{}
	switch mediaType {
	case "image":
		msg.ImageMessage = &waProto.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "video":
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "audio":
		msg.AudioMessage = &waProto.AudioMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			PTT:           proto.Bool(false),
		}
	default:
		msg.DocumentMessage = &waProto.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
			Title:         proto.String(name),
		}
	}
	return msg
}
