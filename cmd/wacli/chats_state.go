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
	chat     string
	pick     int
	receipts bool
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
			return runChatState(flags, opts, use, nil, func(ctx context.Context, a chatStateApp, jid types.JID) error {
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
			return runChatState(flags, opts, use, nil, func(ctx context.Context, a chatStateApp, jid types.JID) error {
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
			return runChatState(flags, opts, "mute", nil, func(ctx context.Context, a chatStateApp, jid types.JID) error {
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
			return runChatState(flags, opts, "unmute", nil, func(ctx context.Context, a chatStateApp, jid types.JID) error {
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
			return runChatState(flags, opts, use, &read, func(ctx context.Context, a chatStateApp, jid types.JID) error {
				return a.MarkChatRead(ctx, jid, read)
			})
		},
	}
	addChatStateFlags(cmd, &opts)
	if read {
		cmd.Flags().BoolVar(&opts.receipts, "receipts", false, "send read receipts for up to 100 unread messages without waiting for app-state recovery; follows your read-receipt privacy setting")
	}
	return cmd
}

type chatStateApp interface {
	ArchiveChat(context.Context, types.JID, bool) error
	PinChat(context.Context, types.JID, bool) error
	MuteChat(context.Context, types.JID, bool, time.Duration) error
	MarkChatRead(context.Context, types.JID, bool) error
	MarkChatReadWithReceipts(context.Context, types.JID) (int, types.ReceiptType, error)
}

// Receipt dispatch does not prove that a sender received a notification.
type chatStateReceipts struct {
	count int
	kind  string
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
)

// A separate kind makes old daemons reject receipts instead of ignoring a flag.
func markReadDelegateKind(receipts bool) string {
	if receipts {
		return markReadReceiptsKind
	}
	return markReadKind
}

// explainReceiptsDelegateError turns that rejection into the action to take.
func explainReceiptsDelegateError(err error, requested bool) error {
	if err == nil || !requested || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), markReadReceiptsKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support --receipts and left the chat unread; restart `wacli sync` after upgrading, then run this again: %w", err)
}

func runChatState(flags *rootFlags, opts chatStateOptions, action string, delegateRead *bool, run func(context.Context, chatStateApp, types.JID) error) error {
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
		if delegateRead != nil {
			resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
				Kind:     markReadDelegateKind(opts.receipts),
				To:       opts.chat,
				Pick:     opts.pick,
				Read:     delegateRead,
				Receipts: opts.receipts,
			})
			if delegated {
				if delegateErr != nil {
					return explainReceiptsDelegateError(delegateErr, opts.receipts)
				}
				receipts, err := delegatedReceipts(resp, opts.receipts)
				if err != nil {
					return err
				}
				return writeChatStateResult(flags, action, resp.Chat, receipts)
			}
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
	if opts.receipts {
		n, kind, err := a.MarkChatReadWithReceipts(ctx, jid)
		if err != nil {
			return err
		}
		receipts = &chatStateReceipts{count: n, kind: string(kind)}
	} else if err := run(ctx, a, jid); err != nil {
		return err
	}
	return writeChatStateResult(flags, action, jid.String(), receipts)
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
			if receipts.kind != "" {
				result["receipt"] = receipts.kind
				result["sender_notified"] = receipts.senderNotified()
			}
		}
		return out.WriteJSON(os.Stdout, result)
	}
	if receipts == nil {
		fmt.Fprintf(os.Stdout, "%s: %s\n", action, chat)
		return nil
	}
	note := ""
	if receipts.kind == string(types.ReceiptTypeReadSelf) {
		note = fmt.Sprintf(", sent as %s: your read-receipt privacy setting keeps them from the sender", receipts.kind)
	} else if receipts.count > 0 {
		note = ", sender notification unknown; WhatsApp applies your privacy setting"
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
