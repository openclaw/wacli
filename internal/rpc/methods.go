package rpc

import (
	"context"
	"fmt"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/steipete/wacli/internal/app"
	"go.mau.fi/whatsmeow/types"
)

// newService is a thin helper to reduce boilerplate in handlers.
func newService(a *app.App) *app.Service {
	return app.NewService(a)
}

// ---- send ---------------------------------------------------------------

// SendRequest holds parameters for the "send" RPC method.
type SendRequest struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

// SendResponse is returned by the "send" method.
type SendResponse struct {
	ID        string `json:"id"`
	ChatJID   string `json:"chatJid"`
	Timestamp string `json:"timestamp"`
}

func (s *Server) rpcSend(ctx context.Context, req SendRequest) (SendResponse, error) {
	if req.Recipient == "" {
		return SendResponse{}, &jrpc2.Error{Code: -32602, Message: "recipient is required"}
	}
	if req.Message == "" {
		return SendResponse{}, &jrpc2.Error{Code: -32602, Message: "message is required"}
	}

	jid, err := types.ParseJID(req.Recipient)
	if err != nil {
		return SendResponse{}, &jrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid recipient JID: %v", err)}
	}

	svc := newService(s.app)
	msgID, err := svc.SendText(ctx, jid, req.Message)
	if err != nil {
		return SendResponse{}, &jrpc2.Error{Code: -32011, Message: fmt.Sprintf("send failed: %v", err)}
	}

	return SendResponse{
		ID:        msgID,
		ChatJID:   jid.String(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

// ---- listChats ----------------------------------------------------------

// ListChatsRequest holds parameters for the "listChats" method.
type ListChatsRequest struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// ChatItem is a single chat entry.
type ChatItem struct {
	JID           string `json:"jid"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	LastMessageTS string `json:"lastMessageTs,omitempty"`
}

// ListChatsResponse is the result of "listChats".
type ListChatsResponse struct {
	Chats []ChatItem `json:"chats"`
}

func (s *Server) rpcListChats(ctx context.Context, req ListChatsRequest) (ListChatsResponse, error) {
	svc := newService(s.app)
	chats, err := svc.ListChats(ctx, req.Query, req.Limit)
	if err != nil {
		return ListChatsResponse{}, &jrpc2.Error{Code: -32603, Message: fmt.Sprintf("listChats: %v", err)}
	}

	items := make([]ChatItem, 0, len(chats))
	for _, c := range chats {
		item := ChatItem{JID: c.JID, Kind: c.Kind, Name: c.Name}
		if !c.LastMessageTS.IsZero() {
			item.LastMessageTS = c.LastMessageTS.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	return ListChatsResponse{Chats: items}, nil
}

// ---- getMessages --------------------------------------------------------

// GetMessagesRequest holds parameters for the "getMessages" method.
type GetMessagesRequest struct {
	ChatJID string `json:"chatJid"`
	Limit   int    `json:"limit,omitempty"`
}

// MessageItem represents a single message.
type MessageItem struct {
	ID          string `json:"id"`
	ChatJID     string `json:"chatJid"`
	SenderJID   string `json:"senderJid,omitempty"`
	Timestamp   string `json:"timestamp"`
	FromMe      bool   `json:"fromMe"`
	Text        string `json:"text,omitempty"`
	DisplayText string `json:"displayText,omitempty"`
	MediaType   string `json:"mediaType,omitempty"`
}

// GetMessagesResponse is the result of "getMessages".
type GetMessagesResponse struct {
	Messages []MessageItem `json:"messages"`
}

func (s *Server) rpcGetMessages(ctx context.Context, req GetMessagesRequest) (GetMessagesResponse, error) {
	if req.ChatJID == "" {
		return GetMessagesResponse{}, &jrpc2.Error{Code: -32602, Message: "chatJid is required"}
	}
	svc := newService(s.app)
	msgs, err := svc.GetMessages(ctx, req.ChatJID, req.Limit)
	if err != nil {
		return GetMessagesResponse{}, &jrpc2.Error{Code: -32603, Message: fmt.Sprintf("getMessages: %v", err)}
	}

	items := make([]MessageItem, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, MessageItem{
			ID:          m.MsgID,
			ChatJID:     m.ChatJID,
			SenderJID:   m.SenderJID,
			Timestamp:   m.Timestamp.UTC().Format(time.RFC3339Nano),
			FromMe:      m.FromMe,
			Text:        m.Text,
			DisplayText: m.DisplayText,
			MediaType:   m.MediaType,
		})
	}
	return GetMessagesResponse{Messages: items}, nil
}

// ---- subscribe ----------------------------------------------------------

// SubscribeResponse is returned by the "subscribe" method.
type SubscribeResponse struct {
	ID string `json:"id"`
}

// rpcSubscribe registers the calling client to receive event notifications.
// Events are pushed as server-side notifications with method "event".
func (s *Server) rpcSubscribe(ctx context.Context) (SubscribeResponse, error) {
	srv := jrpc2.ServerFromContext(ctx)
	id, evCh, cancel := s.hub.Subscribe()

	go func() {
		defer cancel()
		for evt := range evCh {
			if err := srv.Notify(context.Background(), "event", evt); err != nil {
				// Client disconnected or server stopped.
				return
			}
		}
	}()

	return SubscribeResponse{ID: id}, nil
}

// (no additional helpers needed)
