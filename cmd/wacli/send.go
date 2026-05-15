package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/linkpreview"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func newSendCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send messages",
	}
	cmd.AddCommand(newSendTextCmd(flags))
	cmd.AddCommand(newSendFileCmd(flags))
	cmd.AddCommand(newSendStickerCmd(flags))
	cmd.AddCommand(newSendVoiceCmd(flags))
	cmd.AddCommand(newSendReactCmd(flags))
	cmd.AddCommand(newSendPollCmd(flags))
	return cmd
}

func newSendTextCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var message string
	var mentions []string
	var replyTo string
	var replyToSender string
	var noPreview bool
	var ephemeral bool
	var ephemeralDuration string
	var messageEscapes bool
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "text",
		Short: "Send a text message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || message == "" {
				return fmt.Errorf("--to and --message are required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			if messageEscapes {
				decoded, err := decodeMessageEscapes(message)
				if err != nil {
					return err
				}
				message = decoded
			}
			ephemeralOpts := textEphemeralOptions{
				Enabled:     ephemeral,
				Duration:    ephemeralDuration,
				DurationSet: cmd.Flags().Changed("ephemeral-duration"),
			}
			if err := validateTextEphemeralOptions(ephemeralOpts); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:                 "text",
					To:                   to,
					Pick:                 pick,
					Message:              message,
					Mentions:             mentions,
					ReplyTo:              replyTo,
					ReplyToSender:        replyToSender,
					NoPreview:            noPreview,
					Ephemeral:            ephemeralOpts.Enabled,
					EphemeralDuration:    ephemeralOpts.Duration,
					EphemeralDurationSet: ephemeralOpts.DurationSet,
					PostSendWaitMS:       durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "text", resp)
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}

			toJID, err := resolveRecipient(a, to, recipientOptions{pick: pick, asJSON: flags.asJSON})
			if err != nil {
				return err
			}
			mentionedJIDs, err := parseMentionedJIDs(mentions)
			if err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			toJID = warmupRecipient(ctx, a.WA(), toJID, os.Stderr)
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}

			preview := fetchLinkPreview(ctx, message, noPreview)
			msgID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return sendTextMessage(ctx, a, toJID, message, replyTo, replyToSender, preview, mentionedJIDs, ephemeralOpts)
			})
			if err != nil {
				return err
			}

			now := time.Now().UTC()
			chat := toJID
			chatName := a.WA().ResolveChatName(ctx, chat, "")
			kind := chatKindFromJID(chat)
			_ = a.DB().UpsertChat(chat.String(), kind, chatName, now)
			_ = a.DB().UpsertMessage(store.UpsertMessageParams{
				ChatJID:    chat.String(),
				ChatName:   chatName,
				MsgID:      string(msgID),
				SenderJID:  "",
				SenderName: "me",
				Timestamp:  now,
				FromMe:     true,
				Text:       message,
			})

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent": true,
					"to":   chat.String(),
					"id":   msgID,
				})
			}
			fmt.Fprintf(os.Stdout, "Sent to %s (id %s)\n", chat.String(), msgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&message, "message", "", "message text")
	cmd.Flags().StringArrayVar(&mentions, "mention", nil, "phone number or user JID to mention (repeatable)")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "message ID to quote/reply to")
	cmd.Flags().StringVar(&replyToSender, "reply-to-sender", "", "sender JID of the quoted message (required for unsynced group replies)")
	cmd.Flags().BoolVar(&noPreview, "no-preview", false, "disable automatic link previews for the first URL in text")
	cmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "send with the disappearing-message timer for this chat")
	cmd.Flags().StringVar(&ephemeralDuration, "ephemeral-duration", "", "disappearing-message timer override (for example 24h, 7d, 90d, 168h)")
	cmd.Flags().BoolVar(&messageEscapes, "message-escapes", false, `interpret backslash escapes in --message (\n, \r, \t, \\, \")`)
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

type sendTextApp interface {
	WA() app.WAClient
	DB() *store.DB
}

type textMessageSender interface {
	SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error)
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
}

type textEphemeralOptions struct {
	Enabled     bool
	Duration    string
	DurationSet bool
}

type resolvedTextEphemeral struct {
	enabled       bool
	hasExpiration bool
	expiration    uint32
}

const defaultEphemeralExpiration uint32 = 7 * 24 * 60 * 60

func sendTextMessage(ctx context.Context, a sendTextApp, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentionedJIDs []string, ephemeral textEphemeralOptions) (types.MessageID, error) {
	return sendTextMessageWithSender(ctx, a.WA(), a.DB(), to, text, replyTo, replyToSender, preview, mentionedJIDs, ephemeral)
}

func sendTextMessageWithSender(ctx context.Context, sender textMessageSender, db *store.DB, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentionedJIDs []string, ephemeral textEphemeralOptions) (types.MessageID, error) {
	msg, plainText, err := buildTextMessage(db, to, text, replyTo, replyToSender, preview, mentionedJIDs)
	if err != nil {
		return "", err
	}
	resolved, err := resolveTextEphemeral(ctx, sender, to, ephemeral)
	if err != nil {
		return "", err
	}
	if plainText && !resolved.enabled {
		return sender.SendText(ctx, to, text)
	}
	if plainText {
		msg = &waProto.Message{
			ExtendedTextMessage: &waProto.ExtendedTextMessage{
				Text: proto.String(text),
			},
		}
	}
	if resolved.hasExpiration {
		applyEphemeralContext(msg, resolved.expiration)
	}
	return sender.SendProtoMessage(ctx, to, msg)
}

func resolveTextEphemeral(ctx context.Context, sender textMessageSender, to types.JID, opts textEphemeralOptions) (resolvedTextEphemeral, error) {
	if err := validateTextEphemeralOptions(opts); err != nil {
		return resolvedTextEphemeral{}, err
	}
	duration := strings.TrimSpace(opts.Duration)
	if duration != "" {
		expiration, err := parseEphemeralExpiration(duration)
		if err != nil {
			return resolvedTextEphemeral{}, err
		}
		return resolvedTextEphemeral{enabled: true, hasExpiration: true, expiration: expiration}, nil
	}
	if !opts.Enabled {
		return resolvedTextEphemeral{}, nil
	}
	if to.Server == types.GroupServer {
		info, _ := sender.GetGroupInfo(ctx, to)
		if info != nil && info.IsEphemeral && info.DisappearingTimer > 0 {
			return resolvedTextEphemeral{enabled: true, hasExpiration: true, expiration: info.DisappearingTimer}, nil
		}
	}
	return resolvedTextEphemeral{enabled: true, hasExpiration: true, expiration: defaultEphemeralExpiration}, nil
}

func validateTextEphemeralOptions(opts textEphemeralOptions) error {
	duration := strings.TrimSpace(opts.Duration)
	if !opts.DurationSet && duration == "" {
		return nil
	}
	if duration == "" {
		return fmt.Errorf("--ephemeral-duration must be a positive duration such as 24h, 7d, 90d, or 168h")
	}
	_, err := parseEphemeralExpiration(duration)
	return err
}

func parseEphemeralExpiration(s string) (uint32, error) {
	d, err := parseEphemeralDuration(s)
	if err != nil {
		return 0, err
	}
	seconds := int64(d / time.Second)
	if seconds <= 0 || seconds > int64(^uint32(0)) {
		return 0, fmt.Errorf("--ephemeral-duration must fit in uint32 seconds")
	}
	return uint32(seconds), nil
}

func parseEphemeralDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("--ephemeral-duration is required")
	}
	switch strings.ReplaceAll(s, " ", "") {
	case "1d", "1day", "day":
		return 24 * time.Hour, nil
	case "7d", "7day", "7days", "1w", "1week", "week":
		return 7 * 24 * time.Hour, nil
	case "90d", "90day", "90days":
		return 90 * 24 * time.Hour, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "d")), 64)
		if err == nil && days > 0 {
			return time.Duration(days * float64(24*time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--ephemeral-duration must be a positive duration such as 24h, 7d, 90d, or 168h")
	}
	return d, nil
}

func applyEphemeralContext(msg *waProto.Message, expiration uint32) {
	if msg == nil || expiration == 0 {
		return
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		if ext.ContextInfo == nil {
			ext.ContextInfo = &waProto.ContextInfo{}
		}
		ext.ContextInfo.Expiration = proto.Uint32(expiration)
	}
}

func fetchLinkPreview(ctx context.Context, text string, disabled bool) *linkpreview.Preview {
	if disabled {
		return nil
	}
	rawURL := linkpreview.FindFirstHTTPURL(text)
	if rawURL == "" {
		return nil
	}
	previewCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	preview, err := linkpreview.Fetch(previewCtx, nil, rawURL)
	if err != nil {
		return nil
	}
	return preview
}

func decodeMessageEscapes(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf(`unfinished escape sequence in --message; supported escapes: \n, \r, \t, \\, \"`)
		}
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		default:
			return "", fmt.Errorf(`unsupported escape sequence \%c in --message; supported escapes: \n, \r, \t, \\, \"`, s[i])
		}
	}
	return b.String(), nil
}

func buildTextMessage(db *store.DB, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentionedJIDs []string) (*waProto.Message, bool, error) {
	info, err := buildTextContextInfo(db, to, replyTo, replyToSender, mentionedJIDs)
	if err != nil {
		return nil, false, err
	}
	if info == nil && preview == nil {
		return nil, true, nil
	}

	ext := &waProto.ExtendedTextMessage{
		Text:        proto.String(text),
		ContextInfo: info,
	}
	attachLinkPreview(ext, preview)
	return &waProto.Message{ExtendedTextMessage: ext}, false, nil
}

func attachLinkPreview(msg *waProto.ExtendedTextMessage, preview *linkpreview.Preview) {
	if preview == nil {
		return
	}
	if preview.URL != "" {
		msg.MatchedText = proto.String(preview.URL)
	}
	if preview.Title != "" {
		msg.Title = proto.String(preview.Title)
	}
	if preview.Description != "" {
		msg.Description = proto.String(preview.Description)
	}
	if len(preview.Thumbnail) > 0 {
		msg.PreviewType = waProto.ExtendedTextMessage_IMAGE.Enum()
		msg.JPEGThumbnail = preview.Thumbnail
		return
	}
	msg.PreviewType = waProto.ExtendedTextMessage_NONE.Enum()
}

func buildTextContextInfo(db *store.DB, chat types.JID, replyTo, replyToSender string, mentionedJIDs []string) (*waProto.ContextInfo, error) {
	info, err := buildReplyContextInfo(db, chat, replyTo, replyToSender)
	if err != nil {
		return nil, err
	}
	if len(mentionedJIDs) == 0 {
		return info, nil
	}
	if info == nil {
		info = &waProto.ContextInfo{}
	}
	info.MentionedJID = append([]string(nil), mentionedJIDs...)
	return info, nil
}

func buildReplyContextInfo(db *store.DB, chat types.JID, replyTo, replyToSender string) (*waProto.ContextInfo, error) {
	replyTo = strings.TrimSpace(replyTo)
	if replyTo == "" {
		return nil, nil
	}

	sender, err := resolveReplySender(db, chat, replyTo, replyToSender)
	if err != nil {
		return nil, err
	}

	stanzaID := replyTo
	info := &waProto.ContextInfo{StanzaID: proto.String(stanzaID)}
	if !sender.IsEmpty() {
		participant := sender.String()
		info.Participant = proto.String(participant)
	}
	return info, nil
}

func resolveReplySender(db *store.DB, chat types.JID, replyTo, override string) (types.JID, error) {
	if strings.TrimSpace(override) != "" {
		jid, err := wa.ParseUserOrJID(override)
		if err != nil {
			return types.JID{}, fmt.Errorf("invalid --reply-to-sender: %w", err)
		}
		return jid, nil
	}

	msg, err := db.GetMessage(chat.String(), replyTo)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return types.JID{}, fmt.Errorf("lookup quoted message: %w", err)
	}
	if err == nil && strings.TrimSpace(msg.SenderJID) != "" {
		jid, err := types.ParseJID(msg.SenderJID)
		if err != nil {
			return types.JID{}, fmt.Errorf("stored quoted sender is invalid: %w", err)
		}
		return jid, nil
	}

	if chat.Server == types.GroupServer {
		return types.JID{}, fmt.Errorf("--reply-to-sender is required for unsynced group replies")
	}
	return types.JID{}, nil
}

func parseMentionedJIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		jid, err := wa.ParseUserOrJID(value)
		if err != nil {
			return nil, fmt.Errorf("invalid --mention: %w", err)
		}
		if jid.Server == types.GroupServer {
			return nil, fmt.Errorf("invalid --mention %q: mentions must target a user phone number or user JID", value)
		}
		normalized := jid.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}
