package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

// deleteChatKind is its own delegate kind, so an older follow process rejects
// it as unsupported instead of reading it as some other request.
const deleteChatKind = "delete_chat"

// deleteChatCode is the code a delete that reached WhatsApp answers with,
// beside the refusal codes app.ChatDelete*.
const deleteChatCode = "deleted"

func newChatsDeleteCmd(flags *rootFlags) *cobra.Command {
	var chat, through string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a one-to-one chat for you, through a time",
		Long: "Delete a one-to-one chat from WhatsApp on all your devices (not for the other person), " +
			"through the newest stored message at or before --through; messages after it stay. " +
			"Refused when a stored message is later than --through. The local store keeps the chat.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatsDelete(flags, chat, through)
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "JID or phone number of a one-to-one chat (names are not resolved)")
	cmd.Flags().StringVar(&through, "through", "", "RFC 3339 time the delete reaches through, for example 2026-10-22T09:30:00Z")
	return cmd
}

func runChatsDelete(flags *rootFlags, chat, through string) error {
	jid, err := parseDeleteChatTarget(chat)
	if err != nil {
		return err
	}
	at, err := parseDeleteChatThrough(through)
	if err != nil {
		return err
	}
	if err := flags.requireWritable(); err != nil {
		return err
	}

	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: deleteChatKind, To: jid.String(), Through: at.Format(time.RFC3339Nano)})
		if !delegated {
			return err
		}
		if delegateErr != nil {
			return explainDeleteChatDelegateError(delegateErr)
		}
		return writeChatDeleteResult(flags, resp.Chat, resp.LastMessageID, resp.DeletedThrough)
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	removePersistenceHandler, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// As runChatState: close the socket before the handler goes, so
		// App.Close drains every persistence task first.
		a.WA().Disconnect()
		removePersistenceHandler()
	}()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	deleted, err := a.DeleteChat(ctx, jid, at)
	if err != nil {
		return err
	}
	return writeChatDeleteResult(flags, deleted.Chat.String(), deleted.LastMessageID, formatDeleteChatTime(deleted.Through))
}

// parseDeleteChatTarget takes a JID or a phone number, never a name: a fuzzy
// match must not pick the chat a delete reaches.
func parseDeleteChatTarget(raw string) (types.JID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return types.JID{}, fmt.Errorf("--chat is required")
	}
	jid, err := wa.ParseUserOrJID(raw)
	if err != nil {
		return types.JID{}, fmt.Errorf("--chat must be a JID or a phone number: %w", err)
	}
	return jid, nil
}

func parseDeleteChatThrough(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, fmt.Errorf("--through is required")
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("--through must be an RFC 3339 time such as 2026-10-22T09:30:00Z: %w", err)
	}
	return at, nil
}

func formatDeleteChatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

type delegatedDeleteChatApp interface {
	DeleteChat(context.Context, types.JID, time.Time) (app.ChatDeleted, error)
}

func executeDelegatedDeleteChat(ctx context.Context, a delegatedDeleteChatApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	jid, err := parseDeleteChatTarget(req.To)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	at, err := parseDeleteChatThrough(req.Through)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	deleted, err := a.DeleteChat(ctx, jid, at)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{
		OK:             true,
		Code:           deleteChatCode,
		Chat:           deleted.Chat.String(),
		LastMessageID:  deleted.LastMessageID,
		DeletedThrough: formatDeleteChatTime(deleted.Through),
	}, nil
}

func explainDeleteChatDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), deleteChatKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support chats delete and deleted nothing; restart `wacli sync` after upgrading, then run this again: %w", err)
}

func writeChatDeleteResult(flags *rootFlags, chat, lastMessageID, through string) error {
	if flags.asJSON {
		result := map[string]any{
			"ok":              true,
			"action":          "delete",
			"code":            deleteChatCode,
			"chat":            chat,
			"deleted_through": through,
		}
		if lastMessageID != "" {
			result["last_message_id"] = lastMessageID
		}
		return out.WriteJSON(os.Stdout, result)
	}
	if lastMessageID == "" {
		fmt.Fprintf(os.Stdout, "delete: %s through %s (no stored message at or before it)\n", chat, through)
		return nil
	}
	fmt.Fprintf(os.Stdout, "delete: %s through %s (last message %s)\n", chat, through, lastMessageID)
	return nil
}
