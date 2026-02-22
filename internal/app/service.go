package app

import (
	"context"
	"fmt"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// Service is a facade over App that provides a clean API for the RPC layer.
// It avoids direct dependency on CLI helpers from the cmd package.
type Service struct {
	app *App
}

type GroupParticipantInfo struct {
	JID  string
	Name string
}

type GroupInfo struct {
	JID          string
	Name         string
	Participants []GroupParticipantInfo
}

type ContactName struct {
	JID      string
	Name     string
	PushName string
}

// NewService wraps an App in a Service.
func NewService(a *App) *Service {
	return &Service{app: a}
}

// SendText sends a plain-text message to the given JID and returns the message ID.
func (s *Service) SendText(ctx context.Context, to types.JID, message string) (types.MessageID, error) {
	if err := s.app.EnsureAuthed(); err != nil {
		return "", err
	}
	return s.app.WA().SendText(ctx, to, message)
}

// ListChats returns a list of chats ordered by most-recent message.
// query is an optional substring filter; limit <= 0 uses the store default (50).
func (s *Service) ListChats(ctx context.Context, query string, limit int) ([]store.Chat, error) {
	return s.app.DB().ListChats(query, limit)
}

// GetMessages returns messages in a given chat ordered by newest first.
// limit <= 0 uses the store default (50).
func (s *Service) GetMessages(ctx context.Context, chatJID string, limit int) ([]store.Message, error) {
	return s.app.DB().ListMessages(store.ListMessagesParams{
		ChatJID: chatJID,
		Limit:   limit,
	})
}

// SendReaction sends a reaction to a message.
// emoji is the reaction string; pass "" to remove an existing reaction.
func (s *Service) SendReaction(ctx context.Context, to types.JID, targetMsgID types.MessageID, emoji string) (types.MessageID, error) {
	if err := s.app.EnsureAuthed(); err != nil {
		return "", err
	}
	return s.app.WA().SendReaction(ctx, to, targetMsgID, emoji)
}

// RemoteDelete revokes/deletes a sent message.
func (s *Service) RemoteDelete(ctx context.Context, to types.JID, targetMsgID types.MessageID) (types.MessageID, error) {
	if err := s.app.EnsureAuthed(); err != nil {
		return "", err
	}
	return s.app.WA().RemoteDelete(ctx, to, targetMsgID)
}

// SendFile uploads and sends a file attachment, persisting it to the local DB.
func (s *Service) SendFile(ctx context.Context, to types.JID, filePath, caption string) (types.MessageID, string, error) {
	if err := s.app.EnsureAuthed(); err != nil {
		return "", "", err
	}
	result, err := s.app.WA().SendFile(ctx, to, filePath, caption, "")
	if err != nil {
		return "", "", fmt.Errorf("sendFile: %w", err)
	}

	now := time.Now().UTC()
	chatName := s.app.WA().ResolveChatName(ctx, to, "")
	kind := chatKindFromJID(to)
	_ = s.app.DB().UpsertChat(to.String(), kind, chatName, now)
	_ = s.app.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:       to.String(),
		ChatName:      chatName,
		MsgID:         string(result.MsgID),
		SenderJID:     "",
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		Text:          caption,
		MediaType:     result.MediaType,
		MediaCaption:  caption,
		Filename:      result.Filename,
		MimeType:      result.MimeType,
		DirectPath:    result.DirectPath,
		MediaKey:      result.MediaKey,
		FileSHA256:    result.FileSHA256,
		FileEncSHA256: result.FileEncSHA256,
		FileLength:    result.FileLength,
	})

	return result.MsgID, result.MimeType, nil
}

// SearchMessages searches messages by full-text query.
// limit <= 0 defaults to 50.
func (s *Service) SearchMessages(ctx context.Context, query string, limit int) ([]store.Message, error) {
	return s.app.DB().SearchMessages(store.SearchMessagesParams{
		Query: query,
		Limit: limit,
	})
}

// GetGroupInfo fetches live group info and resolves participant display names.
func (s *Service) GetGroupInfo(ctx context.Context, groupJID string) (GroupInfo, error) {
	if err := s.app.EnsureAuthed(); err != nil {
		return GroupInfo{}, err
	}
	jid, err := types.ParseJID(groupJID)
	if err != nil {
		return GroupInfo{}, fmt.Errorf("invalid group JID: %w", err)
	}
	info, err := s.app.WA().GetGroupInfo(ctx, jid)
	if err != nil {
		return GroupInfo{}, err
	}
	if info == nil {
		return GroupInfo{}, fmt.Errorf("group not found")
	}
	out := GroupInfo{
		JID:  info.JID.String(),
		Name: info.GroupName.Name,
	}
	if out.Name == "" {
		out.Name = out.JID
	}
	out.Participants = make([]GroupParticipantInfo, 0, len(info.Participants))
	for _, p := range info.Participants {
		name := ""
		if c, err := s.app.WA().GetContact(ctx, p.JID.ToNonAD()); err == nil {
			name = wa.BestContactName(c)
		}
		if name == "" {
			name = p.JID.String()
		}
		out.Participants = append(out.Participants, GroupParticipantInfo{
			JID:  p.JID.String(),
			Name: name,
		})
	}
	return out, nil
}

// GetContactName fetches a contact's display name from the whatsmeow contact store.
func (s *Service) GetContactName(ctx context.Context, jidStr string) (ContactName, error) {
	if err := s.app.EnsureAuthed(); err != nil {
		return ContactName{}, err
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return ContactName{}, fmt.Errorf("invalid JID: %w", err)
	}
	info, err := s.app.WA().GetContact(ctx, jid.ToNonAD())
	if err != nil {
		return ContactName{}, err
	}
	return ContactName{
		JID:      jid.String(),
		Name:     wa.BestContactName(info),
		PushName: info.PushName,
	}, nil
}

// chatKindFromJID returns the kind string for a JID.
func chatKindFromJID(j types.JID) string {
	if j.Server == types.GroupServer {
		return "group"
	}
	if j.IsBroadcastList() {
		return "broadcast"
	}
	if j.Server == types.DefaultUserServer {
		return "dm"
	}
	return "unknown"
}
