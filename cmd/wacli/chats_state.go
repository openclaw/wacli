package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

type chatStateOptions struct {
	chat             string
	pick             int
	receipts         bool
	throughMessageID string
	messageIDs       []string
}

func newChatsArchiveCmd(flags *rootFlags, archive bool) *cobra.Command {
	use, short := "archive", "Archive a chat"
	if !archive {
		use, short = "unarchive", "Unarchive a chat"
	}
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, use, chatStateDelegateRequest(use, 0), func(ctx context.Context, a chatStateApp, jid types.JID) error {
				return a.ArchiveChat(ctx, jid, archive)
			})
		},
	}
	addChatStateFlags(cmd, &opts)
	return cmd
}

func newChatsPinCmd(flags *rootFlags, pin bool) *cobra.Command {
	use, short := "pin", "Pin a chat"
	if !pin {
		use, short = "unpin", "Unpin a chat"
	}
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, use, chatStateDelegateRequest(use, 0), func(ctx context.Context, a chatStateApp, jid types.JID) error {
				return a.PinChat(ctx, jid, pin)
			})
		},
	}
	addChatStateFlags(cmd, &opts)
	return cmd
}

func newChatsMuteCmd(flags *rootFlags) *cobra.Command {
	opts := chatStateOptions{}
	var duration time.Duration
	cmd := &cobra.Command{
		Use:   "mute",
		Short: "Mute a chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, "mute", chatStateDelegateRequest("mute", duration), func(ctx context.Context, a chatStateApp, jid types.JID) error {
				return a.MuteChat(ctx, jid, true, duration)
			})
		},
	}
	addChatStateFlags(cmd, &opts)
	cmd.Flags().DurationVar(&duration, "duration", 0, "mute duration (for example 8h, 24h, 168h); 0 means forever")
	return cmd
}

func newChatsUnmuteCmd(flags *rootFlags) *cobra.Command {
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   "unmute",
		Short: "Unmute a chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, "unmute", chatStateDelegateRequest("unmute", 0), func(ctx context.Context, a chatStateApp, jid types.JID) error {
				return a.MuteChat(ctx, jid, false, 0)
			})
		},
	}
	addChatStateFlags(cmd, &opts)
	return cmd
}

func newChatsMarkReadCmd(flags *rootFlags, read bool) *cobra.Command {
	use, short := "mark-read", "Mark a chat as read"
	if !read {
		use, short = "mark-unread", "Mark a chat as unread"
	}
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("through-message-id") && (strings.TrimSpace(opts.throughMessageID) == "" || opts.throughMessageID != strings.TrimSpace(opts.throughMessageID)) {
				return fmt.Errorf("--through-message-id requires an exact nonempty message ID")
			}
			if cmd.Flags().Changed("message-id") {
				if err := validateExactReadIDs(opts.messageIDs, opts.receipts, opts.throughMessageID); err != nil {
					return err
				}
			}
			delegate := sendDelegateRequest{Kind: markReadDelegateKind(opts.receipts, opts.throughMessageID), Read: &read, Receipts: opts.receipts, ThroughMessageID: opts.throughMessageID}
			if len(opts.messageIDs) > 0 {
				delegate = sendDelegateRequest{Kind: markReadIDsKind, Read: &read, Receipts: true, MessageIDs: opts.messageIDs}
			}
			return runChatState(flags, opts, use, delegate, func(ctx context.Context, a chatStateApp, jid types.JID) error {
				if opts.throughMessageID != "" {
					return a.MarkChatReadThrough(ctx, jid, opts.throughMessageID)
				}
				return a.MarkChatRead(ctx, jid, read)
			})
		},
	}
	addChatStateFlags(cmd, &opts)
	if read {
		cmd.Flags().StringVar(&opts.throughMessageID, "through-message-id", "", "mark read only through this exact stored inbound message ID")
		cmd.Flags().BoolVar(&opts.receipts, "receipts", false, "send read receipts for up to 100 unread messages without waiting for app-state recovery; follows your read-receipt privacy setting")
		cmd.Flags().StringArrayVar(&opts.messageIDs, "message-id", nil, "with --receipts, acknowledge exactly this stored inbound message (repeatable, up to 100); together they must be the chat's oldest unread messages")
	}
	return cmd
}

type chatStateApp interface {
	ArchiveChat(context.Context, types.JID, bool) error
	PinChat(context.Context, types.JID, bool) error
	MuteChat(context.Context, types.JID, bool, time.Duration) error
	MarkChatRead(context.Context, types.JID, bool) error
	MarkChatReadThrough(context.Context, types.JID, string) error
	MarkChatReadWithReceipts(context.Context, types.JID) (int, types.ReceiptType, error)
	MarkChatReadWithReceiptsThrough(context.Context, types.JID, string) (int, types.ReceiptType, error)
}

// Receipt dispatch does not prove that a sender received a notification.
type chatStateReceipts struct {
	count int
	kind  string
	// settled is set for exact message IDs: whether the local unread count was
	// lowered, or kept because the chat changed while receipts were sent.
	settled *bool
}

func (r chatStateReceipts) senderNotified() *bool {
	if r.kind != string(types.ReceiptTypeReadSelf) {
		return nil
	}
	notified := false
	return &notified
}

const (
	markReadKind         = "mark_read"
	markReadReceiptsKind = "mark_read_receipts"
	markReadThroughKind  = "mark_read_through"
	markReadIDsKind      = "mark_read_ids"
	chatStateKind        = "chat_state"
)

func chatStateDelegateRequest(action string, muteDuration time.Duration) sendDelegateRequest {
	return sendDelegateRequest{
		Kind:            chatStateKind,
		ChatStateAction: action,
		MuteDurationMS:  durationMillis(muteDuration),
	}
}

// A separate kind makes old daemons reject receipts instead of ignoring a flag.
func markReadDelegateKind(receipts bool, throughID string) string {
	// Older owners must reject the whole bounded operation, including receipts,
	// rather than ignore a new JSON field and acknowledge newer messages.
	if throughID != "" {
		return markReadThroughKind
	}
	if receipts {
		return markReadReceiptsKind
	}
	return markReadKind
}

// validateExactReadIDs checks --message-id before anything is sent.
func validateExactReadIDs(ids []string, receipts bool, throughID string) error {
	if !receipts {
		return fmt.Errorf("--message-id requires --receipts")
	}
	if throughID != "" {
		return fmt.Errorf("--message-id and --through-message-id are mutually exclusive")
	}
	if len(ids) == 0 || len(ids) > 100 {
		return fmt.Errorf("--message-id takes 1 to 100 message IDs")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return fmt.Errorf("--message-id requires exact nonempty message IDs")
		}
		if seen[id] {
			return fmt.Errorf("--message-id %q is repeated", id)
		}
		seen[id] = true
	}
	return nil
}

func explainExactReadDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), markReadIDsKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support --message-id and left the chat unread; restart `wacli sync` after upgrading, then run this again: %w", err)
}

func explainReadBoundaryDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), markReadThroughKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support --through-message-id and left the chat unread; restart `wacli sync` after upgrading, then run this again: %w", err)
}

// explainReceiptsDelegateError turns that rejection into the action to take.
func explainReceiptsDelegateError(err error, requested bool) error {
	if err == nil || !requested || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), markReadReceiptsKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support --receipts and left the chat unread; restart `wacli sync` after upgrading, then run this again: %w", err)
}

func explainChatStateDelegateError(err error, action string) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), chatStateKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support delegated chats %s; restart `wacli sync` after upgrading, then run this again: %w", action, err)
}

func runChatState(flags *rootFlags, opts chatStateOptions, action string, delegate sendDelegateRequest, run func(context.Context, chatStateApp, types.JID) error) error {
	if strings.TrimSpace(opts.chat) == "" {
		return fmt.Errorf("--chat is required")
	}
	if err := flags.requireWritable(); err != nil {
		return err
	}

	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		delegate.To = opts.chat
		delegate.Pick = opts.pick
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, delegate)
		if delegated {
			if delegateErr != nil {
				if delegate.Kind == markReadThroughKind {
					return explainReadBoundaryDelegateError(delegateErr)
				}
				if delegate.Kind == markReadIDsKind {
					return explainExactReadDelegateError(delegateErr)
				}
				if delegate.Kind == chatStateKind {
					return explainChatStateDelegateError(delegateErr, action)
				}
				return explainReceiptsDelegateError(delegateErr, opts.receipts)
			}
			receipts, err := delegatedReceipts(resp, opts.receipts)
			if err != nil {
				return err
			}
			if receipts != nil && len(opts.messageIDs) > 0 {
				if resp.UnreadSettled == nil {
					return fmt.Errorf("the running sync process did not report whether local unread state was settled for %s; restart `wacli sync` after upgrading", resp.Chat)
				}
				receipts.settled = resp.UnreadSettled
			}
			return writeChatStateResult(flags, action, resp.Chat, receipts)
		}
		return err
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
		// Keep the handler active until the socket is closed, then let App.Close
		// drain every persistence task before it closes the local database.
		a.WA().Disconnect()
		removePersistenceHandler()
	}()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}

	jid, err := resolveRecipient(a, opts.chat, recipientOptions{pick: opts.pick, asJSON: flags.asJSON})
	if err != nil {
		return err
	}
	// Receipt mode owns its local read boundary and bypasses app-state recovery.
	var receipts *chatStateReceipts
	if len(opts.messageIDs) > 0 {
		n, kind, settled, err := a.MarkChatMessagesReadWithReceipts(ctx, jid, opts.messageIDs)
		if err != nil {
			return err
		}
		receipts = &chatStateReceipts{count: n, kind: string(kind), settled: &settled}
	} else if opts.receipts {
		n, kind, err := markReadReceipts(ctx, a, jid, opts.throughMessageID)
		if err != nil {
			return err
		}
		receipts = &chatStateReceipts{count: n, kind: string(kind)}
	} else if err := run(ctx, a, jid); err != nil {
		return err
	}
	return writeChatStateResult(flags, action, jid.String(), receipts)
}

// Both direct and delegated receipt execution share this dispatch, so adding a
// cutoff cannot accidentally retain the unbounded receipt path in either mode.
func markReadReceipts(ctx context.Context, a interface {
	MarkChatReadWithReceipts(context.Context, types.JID) (int, types.ReceiptType, error)
	MarkChatReadWithReceiptsThrough(context.Context, types.JID, string) (int, types.ReceiptType, error)
}, jid types.JID, throughID string) (int, types.ReceiptType, error) {
	if throughID != "" {
		return a.MarkChatReadWithReceiptsThrough(ctx, jid, throughID)
	}
	return a.MarkChatReadWithReceipts(ctx, jid)
}

// writeChatStateResult prints a state command's result. receipts is set only
// when --receipts was requested.
func writeChatStateResult(flags *rootFlags, action, chat string, receipts *chatStateReceipts) error {
	if flags.asJSON {
		result := map[string]any{
			"ok":     true,
			"action": action,
			"chat":   chat,
		}
		if receipts != nil {
			result["receipts"] = receipts.count
			if receipts.count == 0 {
				result["local_only"] = true
			}
			if receipts.kind != "" {
				result["receipt"] = receipts.kind
				result["sender_notified"] = receipts.senderNotified()
			}
			if receipts.settled != nil {
				result["unread_settled"] = *receipts.settled
			}
		}
		return out.WriteJSON(os.Stdout, result)
	}
	if receipts == nil {
		fmt.Fprintf(os.Stdout, "%s: %s\n", action, chat)
		return nil
	}
	note := ""
	if receipts.count == 0 {
		note = ", local state only; no messages acknowledged"
	} else if receipts.kind == string(types.ReceiptTypeReadSelf) {
		note = fmt.Sprintf(", sent as %s: your read-receipt privacy setting keeps them from the sender", receipts.kind)
	} else if receipts.count > 0 {
		note = ", sender notification unknown; WhatsApp applies your privacy setting"
	}
	if receipts.settled != nil && !*receipts.settled {
		note += "; local unread count kept because the chat changed while sending"
	}
	fmt.Fprintf(os.Stdout, "%s: %s (read receipts: %d%s)\n", action, chat, receipts.count, note)
	return nil
}

func delegatedReceipts(resp sendDelegateResponse, requested bool) (*chatStateReceipts, error) {
	if !requested {
		return nil, nil
	}
	if resp.Receipts == nil {
		return nil, fmt.Errorf("the running sync process did not report receipt results for %s; restart `wacli sync` after upgrading", resp.Chat)
	}
	return &chatStateReceipts{count: *resp.Receipts, kind: resp.ReceiptType}, nil
}

func addChatStateFlags(cmd *cobra.Command, opts *chatStateOptions) {
	cmd.Flags().StringVar(&opts.chat, "chat", "", "chat name, phone number, or JID")
	cmd.Flags().IntVar(&opts.pick, "pick", 0, "choose match N when --chat is ambiguous")
}

func validateBoolFilter(name string, pos, neg bool) error {
	if pos && neg {
		return fmt.Errorf("--%s and --no-%s are mutually exclusive", name, name)
	}
	return nil
}

func boolFilter(pos, neg bool) *bool {
	if pos {
		v := true
		return &v
	}
	if neg {
		v := false
		return &v
	}
	return nil
}

func chatFlagsString(c store.Chat) string {
	var flags []string
	if c.Pinned {
		flags = append(flags, "pinned")
	}
	if c.Archived {
		flags = append(flags, "archived")
	}
	if c.Muted() {
		flags = append(flags, "muted")
	}
	if c.Unread {
		if c.UnreadCount > 0 {
			flags = append(flags, fmt.Sprintf("unread:%d", c.UnreadCount))
		} else {
			flags = append(flags, "unread")
		}
	}
	return strings.Join(flags, ",")
}

func formatMutedUntil(until int64) string {
	switch {
	case until == -1:
		return "forever"
	case until > 0:
		return time.Unix(until, 0).Local().Format(time.RFC3339)
	default:
		return ""
	}
}
