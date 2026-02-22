package app

import (
	"context"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

// Service is a facade over App that provides a clean API for the RPC layer.
// It avoids direct dependency on CLI helpers from the cmd package.
type Service struct {
	app *App
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
// query is an optional substring filter; limit ≤ 0 uses the store default (50).
func (s *Service) ListChats(ctx context.Context, query string, limit int) ([]store.Chat, error) {
	return s.app.DB().ListChats(query, limit)
}

// GetMessages returns messages in a given chat ordered by newest first.
// limit ≤ 0 uses the store default (50).
func (s *Service) GetMessages(ctx context.Context, chatJID string, limit int) ([]store.Message, error) {
	return s.app.DB().ListMessages(store.ListMessagesParams{
		ChatJID: chatJID,
		Limit:   limit,
	})
}
