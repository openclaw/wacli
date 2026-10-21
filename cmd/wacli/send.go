package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
	cmd.AddCommand(newSendLocationCmd(flags))
	cmd.AddCommand(newSendPollCmd(flags))
	cmd.AddCommand(newSendStatusCmd(flags))
	cmd.AddCommand(newSendSelectCmd(flags))
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
	var allowSelf bool
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
					AllowSelf:            allowSelf,
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

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}

			toJID, err := resolveRecipient(a, to, recipientOptions{pick: pick, asJSON: flags.asJSON})
			if err != nil {
				return err
			}
			if !allowSelf {
				if err := validateTextRecipient(a.WA(), toJID); err != nil {
					return err
				}
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

			preview, previewWarning := fetchLinkPreview(ctx, message, noPreview)
			msgID, storeErr, err := sendTextWithHistory(ctx, a, reconnectForSend(a), toJID, message, replyTo, replyToSender, preview, mentionedJIDs, ephemeralOpts, textSendOptions{allowSelf: allowSelf})
			if err != nil {
				return err
			}
			warnLinkPreviewSkipped(os.Stderr, previewWarning)
			chat := toJID
			warnSendStoreFailure(os.Stderr, string(msgID), storeErr)

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, addStoreWarning(map[string]any{
					"sent": true,
					"to":   chat.String(),
					"id":   msgID,
				}, storeErr))
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
	cmd.Flags().BoolVar(&allowSelf, "allow-self", false, "allow sending to the linked account itself (delivery is not guaranteed)")
	cmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "send with the disappearing-message timer for this chat")
	cmd.Flags().StringVar(&ephemeralDuration, "ephemeral-duration", "", "disappearing-message timer override (for example 24h, 7d, 90d, 168h)")
	cmd.Flags().BoolVar(&messageEscapes, "message-escapes", false, `interpret backslash escapes in --message (\n, \r, \t, \\, \")`)
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

type outboundTextResolver interface {
	ResolveChatName(ctx context.Context, chat types.JID, pushName string) string
	ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID
}

func canonicalOutboundChat(ctx context.Context, resolver outboundTextResolver, chat types.JID) types.JID {
	chat = resolver.ResolveLIDToPN(ctx, chat)
	if chat.Server == types.DefaultUserServer {
		chat = chat.ToNonAD()
	}
	return chat
}

// Immediate history follows sync's canonical sender contract. Resolve only
// the stored identity; the native context still describes the wire quote.
func canonicalOutboundQuoteSender(ctx context.Context, resolver outboundTextResolver, raw string) string {
	if raw == "" {
		return ""
	}
	jid, err := types.ParseJID(raw)
	if err != nil {
		return raw
	}
	if jid.Server == types.HiddenUserServer {
		jid = canonicalOutboundChat(ctx, resolver, jid)
	} else if jid.Server == types.DefaultUserServer {
		jid = jid.ToNonAD()
	}
	return jid.String()
}

func persistOutboundTextWith(ctx context.Context, db *store.DB, resolver outboundTextResolver, chat types.JID, msgID, text string, now time.Time, contexts ...*waProto.ContextInfo) error {
	chat = canonicalOutboundChat(ctx, resolver, chat)
	var quote *waProto.ContextInfo
	if len(contexts) > 0 {
		quote = contexts[0]
	}
	chatName := resolver.ResolveChatName(ctx, chat, "")
	var storeErr error
	if err := db.UpsertChat(chat.String(), chatKindFromJID(chat), chatName, now); err != nil {
		storeErr = fmt.Errorf("chat update: %w", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:         chat.String(),
		ChatName:        chatName,
		MsgID:           msgID,
		SenderJID:       "",
		SenderName:      "me",
		Timestamp:       now,
		FromMe:          true,
		Text:            text,
		QuotedMsgID:     quote.GetStanzaID(),
		QuotedSenderJID: canonicalOutboundQuoteSender(ctx, resolver, quote.GetParticipant()),
	}); err != nil {
		storeErr = errors.Join(storeErr, fmt.Errorf("message update: %w", err))
	}
	return storeErr
}

type sendTextApp interface {
	WA() app.WAClient
	DB() *store.DB
}

type textMessageSender interface {
	SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error)
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	ResolvePNToLID(ctx context.Context, jid types.JID) types.JID
	ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID
	LinkedJID() string
	LinkedLID() string
}

type textEphemeralOptions struct {
	Enabled     bool
	Duration    string
	DurationSet bool
}

type textSendOptions struct {
	allowSelf bool
	// Capture the context constructed for the accepted native message. History
	// must not reconstruct its participant from flags or a second store lookup.
	replyContext **waProto.ContextInfo
}

type resolvedTextEphemeral struct {
	enabled       bool
	hasExpiration bool
	expiration    uint32
}

const defaultEphemeralExpiration uint32 = 7 * 24 * 60 * 60

var errSelfTextRecipient = errors.New("send text to the linked account itself is not supported: WhatsApp can acknowledge self-messages without delivering them; use the official Message Yourself chat")

func sendTextMessage(ctx context.Context, a sendTextApp, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentionedJIDs []string, ephemeral textEphemeralOptions, options textSendOptions) (types.MessageID, error) {
	return sendTextMessageWithSender(ctx, a.WA(), a.DB(), to, text, replyTo, replyToSender, preview, mentionedJIDs, ephemeral, options)
}

func sendTextMessageWithSender(ctx context.Context, sender textMessageSender, db *store.DB, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentionedJIDs []string, ephemeral textEphemeralOptions, options textSendOptions) (types.MessageID, error) {
	if !options.allowSelf {
		if err := validateTextRecipient(sender, to); err != nil {
			return "", err
		}
	}
	reply, err := buildReplyContextInfo(ctx, sender, db, to, replyTo, replyToSender)
	if err != nil {
		return "", err
	}
	msg, plainText := buildTextMessage(text, reply, preview, mentionedJIDs)
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
	if options.replyContext != nil {
		*options.replyContext = msg.GetExtendedTextMessage().GetContextInfo()
	}
	return sender.SendProtoMessage(ctx, to, msg)
}

func validateTextRecipient(sender textMessageSender, to types.JID) error {
	if isSelfTextRecipient(sender, to) {
		return errSelfTextRecipient
	}
	return nil
}

func isSelfTextRecipient(sender textMessageSender, to types.JID) bool {
	linked, err := types.ParseJID(strings.TrimSpace(sender.LinkedJID()))
	if err != nil || linked.IsEmpty() {
		return false
	}
	linked = linked.ToNonAD()
	to = to.ToNonAD()
	if to == linked {
		return true
	}
	if to.Server != types.HiddenUserServer || linked.Server != types.DefaultUserServer {
		return false
	}
	linkedLID, err := types.ParseJID(strings.TrimSpace(sender.LinkedLID()))
	return err == nil && !linkedLID.IsEmpty() && linkedLID.ToNonAD() == to
}

func textReplySelfJID(ctx context.Context, sender textMessageSender, db *store.DB, chat, aliasChat types.JID, replyTo, replyToSender string) (string, error) {
	linked := strings.TrimSpace(sender.LinkedJID())
	replyTo = strings.TrimSpace(replyTo)
	if replyTo == "" || strings.TrimSpace(replyToSender) != "" {
		return linked, nil
	}
	quoted, err := getQuotedMessage(db, chat, aliasChat, replyTo)
	if err != nil || !quoted.FromMe {
		return linked, nil
	}

	useLID := chat.Server == types.HiddenUserServer
	if chat.Server == types.GroupServer {
		info, infoErr := sender.GetGroupInfo(ctx, chat)
		if infoErr != nil {
			return "", fmt.Errorf("get group info for quoted outgoing message: %w", infoErr)
		}
		useLID = info != nil && info.AddressingMode == types.AddressingModeLID
	}
	if !useLID {
		return linked, nil
	}

	linkedJID, err := types.ParseJID(linked)
	if err != nil || linkedJID.IsEmpty() {
		return "", fmt.Errorf("linked account JID is unavailable for quoted outgoing message %s", replyTo)
	}
	lid := sender.ResolvePNToLID(ctx, linkedJID)
	if lid.IsEmpty() || lid.Server != types.HiddenUserServer {
		return "", fmt.Errorf("linked account LID is unavailable for quoted outgoing message %s", replyTo)
	}
	return lid.ToNonAD().String(), nil
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

// fetchLinkPreview never writes output itself: delegated sends run it inside
// the sync process, whose stderr may be an NDJSON event stream. A skipped
// preview is returned as a warning for the output-owning caller to render.
func fetchLinkPreview(ctx context.Context, text string, disabled bool) (*linkpreview.Preview, string) {
	if disabled {
		return nil, ""
	}
	rawURL := linkpreview.FindFirstHTTPURL(text)
	if rawURL == "" {
		return nil, ""
	}
	previewCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	preview, err := linkpreview.Fetch(previewCtx, nil, rawURL)
	if err != nil {
		return nil, fmt.Sprintf("link preview skipped for %s: %v", rawURL, err)
	}
	return preview, ""
}

// linkPreviewSkippedHint tells the user the send went through and how to opt
// out of preview fetching when the skip is expected.
const linkPreviewSkippedHint = "message text was still sent without a preview; pass --no-preview to send without fetching one"

// warnLinkPreviewSkipped prints a skipped-preview warning after a successful
// send. The text carries remote, server-controlled error detail, so it is
// sanitized for terminals.
func warnLinkPreviewSkipped(w io.Writer, msg string) {
	if msg == "" {
		return
	}
	fmt.Fprintf(w, "warning: %s (%s)\n", out.SanitizeHuman(msg), linkPreviewSkippedHint)
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

func buildTextMessage(text string, reply *waProto.ContextInfo, preview *linkpreview.Preview, mentionedJIDs []string) (*waProto.Message, bool) {
	info := reply
	if len(mentionedJIDs) > 0 {
		if info == nil {
			info = &waProto.ContextInfo{}
		}
		info.MentionedJID = append([]string(nil), mentionedJIDs...)
	}
	if info == nil && preview == nil {
		return nil, true
	}

	ext := &waProto.ExtendedTextMessage{
		Text:        proto.String(text),
		ContextInfo: info,
	}
	attachLinkPreview(ext, preview)
	return &waProto.Message{ExtendedTextMessage: ext}, false
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

func getQuotedMessage(db *store.DB, chat, aliasChat types.JID, replyTo string) (store.Message, error) {
	quoted, err := db.GetMessage(chat.String(), replyTo)
	if !errors.Is(err, sql.ErrNoRows) || aliasChat.IsEmpty() || aliasChat.String() == chat.String() {
		return quoted, err
	}
	return db.GetMessage(aliasChat.String(), replyTo)
}

func replyLookupAlias(ctx context.Context, sender textMessageSender, to types.JID) types.JID {
	switch to.Server {
	case types.DefaultUserServer:
		return sender.ResolvePNToLID(ctx, to).ToNonAD()
	case types.HiddenUserServer:
		return sender.ResolveLIDToPN(ctx, to).ToNonAD()
	default:
		return types.EmptyJID
	}
}

// buildReplyContextInfo builds the native quote for every send that takes
// --reply-to (text, file, voice, sticker). The stored message is looked up under
// the chat or its phone/LID alias and its content is quoted; an unknown message
// is refused unless --reply-to-sender names its author in a group.
func buildReplyContextInfo(ctx context.Context, sender textMessageSender, db *store.DB, chat types.JID, replyTo, replyToSender string) (*waProto.ContextInfo, error) {
	if strings.TrimSpace(replyTo) == "" {
		return nil, nil
	}
	aliasChat := replyLookupAlias(ctx, sender, chat)
	selfJID, err := textReplySelfJID(ctx, sender, db, chat, aliasChat, replyTo, replyToSender)
	if err != nil {
		return nil, err
	}
	return buildTextReplyContextInfo(db, chat, aliasChat, replyTo, replyToSender, selfJID)
}

func buildTextReplyContextInfo(db *store.DB, chat, aliasChat types.JID, replyTo, replyToSender, selfJID string) (*waProto.ContextInfo, error) {
	replyTo = strings.TrimSpace(replyTo)
	if replyTo == "" {
		return nil, nil
	}

	quoted, err := getQuotedMessage(db, chat, aliasChat, replyTo)
	if errors.Is(err, sql.ErrNoRows) {
		if chat.Server == types.GroupServer && strings.TrimSpace(replyToSender) != "" {
			participant, participantErr := resolveTextReplyParticipant(chat, store.Message{}, replyToSender, selfJID)
			if participantErr != nil {
				return nil, participantErr
			}
			return &waProto.ContextInfo{
				StanzaID:    proto.String(replyTo),
				Participant: proto.String(participant.String()),
			}, nil
		}
		return nil, fmt.Errorf("quoted message %s not found in local store for chat %s; run `wacli sync` first", replyTo, chat.String())
	}
	if err != nil {
		return nil, fmt.Errorf("lookup quoted message: %w", err)
	}
	quotedMessage, err := storedQuotedMessage(db, quoted)
	if err != nil {
		return nil, fmt.Errorf("cannot quote message %s: %w", replyTo, err)
	}
	participant, err := resolveTextReplyParticipant(chat, quoted, replyToSender, selfJID)
	if err != nil {
		return nil, err
	}

	return &waProto.ContextInfo{
		StanzaID:      proto.String(replyTo),
		Participant:   proto.String(participant.String()),
		QuotedMessage: quotedMessage,
	}, nil
}

func storedQuotedMessage(db *store.DB, msg store.Message) (*waProto.Message, error) {
	if msg.Revoked || msg.DeletedForMe {
		return nil, fmt.Errorf("stored message was deleted")
	}
	if msg.ReactionToID != "" || len(msg.Buttons) > 0 {
		return nil, fmt.Errorf("stored message content is not supported for quoted replies")
	}
	if mediaType := strings.ToLower(strings.TrimSpace(msg.MediaType)); mediaType != "" {
		if !isStoredMediaType(mediaType) {
			return nil, fmt.Errorf("unsupported stored media type %q", mediaType)
		}
		mediaInfo, err := db.GetMediaDownloadInfo(msg.ChatJID, msg.MsgID)
		if err != nil {
			return nil, fmt.Errorf("load stored media metadata: %w", err)
		}
		if err := validateStoredMediaInfo(mediaInfo); err != nil {
			return nil, err
		}
		return buildStoredMediaMessage(mediaType, msg.MediaCaption, mediaInfo, nil)
	}
	if strings.TrimSpace(msg.Text) == "" {
		return nil, fmt.Errorf("stored message has no supported text content")
	}
	return &waProto.Message{Conversation: proto.String(msg.Text)}, nil
}

func resolveTextReplyParticipant(chat types.JID, msg store.Message, override, selfJID string) (types.JID, error) {
	if strings.TrimSpace(override) != "" {
		jid, err := wa.ParseUserOrJID(override)
		if err != nil {
			return types.JID{}, fmt.Errorf("invalid --reply-to-sender: %w", err)
		}
		return jid.ToNonAD(), nil
	}
	if msg.FromMe {
		jid, err := types.ParseJID(strings.TrimSpace(selfJID))
		if err != nil || jid.IsEmpty() {
			return types.JID{}, fmt.Errorf("linked account JID is unavailable for quoted outgoing message %s", msg.MsgID)
		}
		return jid.ToNonAD(), nil
	}
	if sender := strings.TrimSpace(msg.SenderJID); sender != "" {
		jid, err := types.ParseJID(sender)
		if err != nil {
			return types.JID{}, fmt.Errorf("stored quoted sender is invalid: %w", err)
		}
		return jid.ToNonAD(), nil
	}
	if chat.Server != types.GroupServer {
		return chat, nil
	}
	return types.JID{}, fmt.Errorf("--reply-to-sender is required because the stored group message has no sender")
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
