package rpc

import (
	"context"
	"fmt"
	"time"

	"github.com/creachadair/jrpc2"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// newService is a thin helper to reduce boilerplate in handlers.
func newService(a *app.App) *app.Service {
	return app.NewService(a)
}

func (s *Server) ensureConnected(ctx context.Context) error {
	if s.app.WA().IsConnected() {
		return nil
	}
	reconnectCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.app.WA().ReconnectWithBackoff(reconnectCtx, 500*time.Millisecond, 5*time.Second); err != nil {
		return fmt.Errorf("not connected and reconnect failed: %w", err)
	}
	return nil
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

	if err := s.ensureConnected(ctx); err != nil {
		return SendResponse{}, &jrpc2.Error{Code: -32011, Message: err.Error()}
	}

	jid, err := types.ParseJID(req.Recipient)
	if err != nil {
		return SendResponse{}, &jrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid recipient JID: %v", err)}
	}
	jid = jid.ToNonAD()
	if wa.IsLIDJID(jid) {
		if resolved, resolveErr := s.app.WA().ResolveRecipientJID(ctx, jid); resolveErr == nil && !resolved.IsEmpty() {
			jid = resolved.ToNonAD()
		}
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
	if srv == nil {
		return SubscribeResponse{}, &jrpc2.Error{Code: -32603, Message: "internal error: server context unavailable"}
	}
	id, evCh, cancel := s.hub.Subscribe()
	if id == "" {
		return SubscribeResponse{}, &jrpc2.Error{Code: -32000, Message: "event hub is closed"}
	}

	connDone := make(chan struct{})
	go func() {
		_ = srv.Wait()
		close(connDone)
	}()

	go func() {
		defer cancel()
		for {
			select {
			case <-connDone:
				// TCP connection is gone; unsubscribe immediately.
				return
			case evt, ok := <-evCh:
				if !ok {
					return
				}
				notifyCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
				err := srv.Notify(notifyCtx, "event", evt)
				done()
				if err != nil {
					// Client disconnected or server stopped.
					return
				}
			}
		}
	}()

	return SubscribeResponse{ID: id}, nil
}

// ---- sendReaction -------------------------------------------------------

// SendReactionRequest holds parameters for the "sendReaction" RPC method.
type SendReactionRequest struct {
	Recipient       string `json:"recipient"`
	TargetMessageID string `json:"targetMessageId"`
	Emoji           string `json:"emoji"`
}

// SendReactionResponse is returned by the "sendReaction" method.
type SendReactionResponse struct {
	OK bool `json:"ok"`
}

func (s *Server) rpcSendReaction(ctx context.Context, req SendReactionRequest) (SendReactionResponse, error) {
	if req.Recipient == "" {
		return SendReactionResponse{}, &jrpc2.Error{Code: -32602, Message: "recipient is required"}
	}
	if req.TargetMessageID == "" {
		return SendReactionResponse{}, &jrpc2.Error{Code: -32602, Message: "targetMessageId is required"}
	}
	if err := s.ensureConnected(ctx); err != nil {
		return SendReactionResponse{}, &jrpc2.Error{Code: -32011, Message: err.Error()}
	}

	jid, err := types.ParseJID(req.Recipient)
	if err != nil {
		return SendReactionResponse{}, &jrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid recipient JID: %v", err)}
	}
	svc := newService(s.app)
	_, err = svc.SendReaction(ctx, jid, types.MessageID(req.TargetMessageID), req.Emoji)
	if err != nil {
		return SendReactionResponse{}, &jrpc2.Error{Code: -32011, Message: fmt.Sprintf("sendReaction failed: %v", err)}
	}
	return SendReactionResponse{OK: true}, nil
}

// ---- remoteDelete -------------------------------------------------------

// RemoteDeleteRequest holds parameters for the "remoteDelete" RPC method.
type RemoteDeleteRequest struct {
	Recipient       string `json:"recipient"`
	TargetMessageID string `json:"targetMessageId"`
}

// RemoteDeleteResponse is returned by the "remoteDelete" method.
type RemoteDeleteResponse struct {
	OK bool `json:"ok"`
}

func (s *Server) rpcRemoteDelete(ctx context.Context, req RemoteDeleteRequest) (RemoteDeleteResponse, error) {
	if req.Recipient == "" {
		return RemoteDeleteResponse{}, &jrpc2.Error{Code: -32602, Message: "recipient is required"}
	}
	if req.TargetMessageID == "" {
		return RemoteDeleteResponse{}, &jrpc2.Error{Code: -32602, Message: "targetMessageId is required"}
	}
	if err := s.ensureConnected(ctx); err != nil {
		return RemoteDeleteResponse{}, &jrpc2.Error{Code: -32011, Message: err.Error()}
	}

	jid, err := types.ParseJID(req.Recipient)
	if err != nil {
		return RemoteDeleteResponse{}, &jrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid recipient JID: %v", err)}
	}
	svc := newService(s.app)
	_, err = svc.RemoteDelete(ctx, jid, types.MessageID(req.TargetMessageID))
	if err != nil {
		return RemoteDeleteResponse{}, &jrpc2.Error{Code: -32011, Message: fmt.Sprintf("remoteDelete failed: %v", err)}
	}
	return RemoteDeleteResponse{OK: true}, nil
}

// ---- sendFile -----------------------------------------------------------

// SendFileRequest holds parameters for the "sendFile" RPC method.
type SendFileRequest struct {
	Recipient string `json:"recipient"`
	FilePath  string `json:"filePath"`
	Caption   string `json:"caption,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
}

// SendFileResponse is returned by the "sendFile" method.
type SendFileResponse struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	MimeType  string `json:"mimeType"`
}

func (s *Server) rpcSendFile(ctx context.Context, req SendFileRequest) (SendFileResponse, error) {
	if req.Recipient == "" {
		return SendFileResponse{}, &jrpc2.Error{Code: -32602, Message: "recipient is required"}
	}
	if req.FilePath == "" {
		return SendFileResponse{}, &jrpc2.Error{Code: -32602, Message: "filePath is required"}
	}
	if err := s.ensureConnected(ctx); err != nil {
		return SendFileResponse{}, &jrpc2.Error{Code: -32011, Message: err.Error()}
	}

	jid, err := types.ParseJID(req.Recipient)
	if err != nil {
		return SendFileResponse{}, &jrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid recipient JID: %v", err)}
	}
	svc := newService(s.app)
	msgID, mimeType, err := svc.SendFile(ctx, jid, req.FilePath, req.Caption)
	if err != nil {
		return SendFileResponse{}, &jrpc2.Error{Code: -32011, Message: fmt.Sprintf("sendFile failed: %v", err)}
	}
	return SendFileResponse{
		ID:        string(msgID),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		MimeType:  mimeType,
	}, nil
}

// ---- searchMessages -----------------------------------------------------

// SearchMessagesRequest holds parameters for the "searchMessages" RPC method.
type SearchMessagesRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// SearchMessageItem represents a single search result message.
type SearchMessageItem struct {
	ID          string `json:"id"`
	ChatJID     string `json:"chatJid"`
	ChatName    string `json:"chatName,omitempty"`
	SenderJID   string `json:"senderJid,omitempty"`
	Timestamp   string `json:"timestamp"`
	FromMe      bool   `json:"fromMe"`
	Text        string `json:"text,omitempty"`
	DisplayText string `json:"displayText,omitempty"`
	MediaType   string `json:"mediaType,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
}

// SearchMessagesResponse is the result of "searchMessages".
type SearchMessagesResponse struct {
	Messages []SearchMessageItem `json:"messages"`
}

func (s *Server) rpcSearchMessages(ctx context.Context, req SearchMessagesRequest) (SearchMessagesResponse, error) {
	if req.Query == "" {
		return SearchMessagesResponse{}, &jrpc2.Error{Code: -32602, Message: "query is required"}
	}
	svc := newService(s.app)
	msgs, err := svc.SearchMessages(ctx, req.Query, req.Limit)
	if err != nil {
		return SearchMessagesResponse{}, &jrpc2.Error{Code: -32603, Message: fmt.Sprintf("searchMessages: %v", err)}
	}
	items := make([]SearchMessageItem, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, SearchMessageItem{
			ID:          m.MsgID,
			ChatJID:     m.ChatJID,
			ChatName:    m.ChatName,
			SenderJID:   m.SenderJID,
			Timestamp:   m.Timestamp.UTC().Format(time.RFC3339Nano),
			FromMe:      m.FromMe,
			Text:        m.Text,
			DisplayText: m.DisplayText,
			MediaType:   m.MediaType,
			Snippet:     m.Snippet,
		})
	}
	return SearchMessagesResponse{Messages: items}, nil
}

// ---- getGroupInfo ------------------------------------------------------

// GetGroupInfoRequest holds parameters for the "getGroupInfo" RPC method.
type GetGroupInfoRequest struct {
	JID string `json:"jid"`
}

// GroupParticipantItem is a single group participant entry.
type GroupParticipantItem struct {
	JID  string `json:"jid"`
	Name string `json:"name"`
}

// GetGroupInfoResponse is the result of "getGroupInfo".
type GetGroupInfoResponse struct {
	JID          string                 `json:"jid"`
	Name         string                 `json:"name"`
	Participants []GroupParticipantItem `json:"participants"`
}

func (s *Server) rpcGetGroupInfo(ctx context.Context, req GetGroupInfoRequest) (GetGroupInfoResponse, error) {
	if req.JID == "" {
		return GetGroupInfoResponse{}, &jrpc2.Error{Code: -32602, Message: "jid is required"}
	}
	if _, err := types.ParseJID(req.JID); err != nil {
		return GetGroupInfoResponse{}, &jrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid jid: %v", err)}
	}

	if err := s.ensureConnected(ctx); err != nil {
		return GetGroupInfoResponse{}, &jrpc2.Error{Code: -32011, Message: err.Error()}
	}

	svc := newService(s.app)
	info, err := svc.GetGroupInfo(ctx, req.JID)
	if err != nil {
		return GetGroupInfoResponse{}, &jrpc2.Error{Code: -32011, Message: fmt.Sprintf("getGroupInfo failed: %v", err)}
	}

	resp := GetGroupInfoResponse{
		JID:          info.JID,
		Name:         info.Name,
		Participants: make([]GroupParticipantItem, 0, len(info.Participants)),
	}
	for _, p := range info.Participants {
		resp.Participants = append(resp.Participants, GroupParticipantItem{
			JID:  p.JID,
			Name: p.Name,
		})
	}
	return resp, nil
}

// ---- getContactName ----------------------------------------------------

// GetContactNameRequest holds parameters for the "getContactName" RPC method.
type GetContactNameRequest struct {
	JID string `json:"jid"`
}

// GetContactNameResponse is the result of "getContactName".
type GetContactNameResponse struct {
	JID      string `json:"jid"`
	Name     string `json:"name"`
	PushName string `json:"pushName"`
}

func (s *Server) rpcGetContactName(ctx context.Context, req GetContactNameRequest) (GetContactNameResponse, error) {
	if req.JID == "" {
		return GetContactNameResponse{}, &jrpc2.Error{Code: -32602, Message: "jid is required"}
	}
	if _, err := types.ParseJID(req.JID); err != nil {
		return GetContactNameResponse{}, &jrpc2.Error{Code: -32602, Message: fmt.Sprintf("invalid jid: %v", err)}
	}

	if err := s.ensureConnected(ctx); err != nil {
		return GetContactNameResponse{}, &jrpc2.Error{Code: -32011, Message: err.Error()}
	}

	svc := newService(s.app)
	contact, err := svc.GetContactName(ctx, req.JID)
	if err != nil {
		return GetContactNameResponse{}, &jrpc2.Error{Code: -32011, Message: fmt.Sprintf("getContactName failed: %v", err)}
	}
	return GetContactNameResponse{
		JID:      contact.JID,
		Name:     contact.Name,
		PushName: contact.PushName,
	}, nil
}
