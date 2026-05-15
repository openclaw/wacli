package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

const (
	pollMinOptions = 2
	pollMaxOptions = 12
)

type pollSender interface {
	SendPoll(ctx context.Context, to types.JID, name string, options []string, selectable int, ephemeral bool) (types.MessageID, error)
}

type outboundPollIdentityResolver interface {
	LinkedJID() string
	ResolvePNToLID(ctx context.Context, jid types.JID) types.JID
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
}

func newSendPollCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var question string
	var options []string
	var multi int
	var ephemeral bool
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Send a poll",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			cleaned, err := validatePollOptions(question, options, multi)
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
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:           "poll",
					To:             to,
					Pick:           pick,
					Question:       question,
					Options:        cleaned,
					Selectable:     multi,
					Ephemeral:      ephemeral,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "poll", resp)
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
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			toJID = warmupRecipient(ctx, a.WA(), toJID, os.Stderr)
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}

			msgID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return sendPollMessage(ctx, a.WA(), toJID, question, cleaned, multi, ephemeral)
			})
			if err != nil {
				return err
			}

			persistOutboundPoll(ctx, a, toJID, string(msgID), question, cleaned, uint32(multi), time.Now().UTC())
			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent":     true,
					"to":       toJID.String(),
					"id":       msgID,
					"question": question,
					"options":  cleaned,
				})
			}
			fmt.Fprintf(os.Stdout, "Sent poll to %s (id %s)\n", toJID.String(), msgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&question, "question", "", "poll question")
	cmd.Flags().StringArrayVar(&options, "option", nil, "poll option (repeat for each option, 2-12 total)")
	cmd.Flags().IntVar(&multi, "multi", 1, "maximum number of options a voter may pick (1 = single-select)")
	cmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "wrap the poll in EphemeralMessage for disappearing-message chats")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

// validatePollOptions trims, deduplicates, and bounds-checks user-supplied
// poll options. Returns the cleaned, ordered option list.
func validatePollOptions(question string, options []string, multi int) ([]string, error) {
	q := strings.TrimSpace(question)
	if q == "" {
		return nil, fmt.Errorf("--question is required")
	}
	cleaned := make([]string, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for _, opt := range options {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		if _, dup := seen[opt]; dup {
			return nil, fmt.Errorf("duplicate option %q", opt)
		}
		seen[opt] = struct{}{}
		cleaned = append(cleaned, opt)
	}
	if len(cleaned) < pollMinOptions {
		return nil, fmt.Errorf("at least %d options are required (got %d)", pollMinOptions, len(cleaned))
	}
	if len(cleaned) > pollMaxOptions {
		return nil, fmt.Errorf("at most %d options are allowed (got %d)", pollMaxOptions, len(cleaned))
	}
	if multi < 1 {
		return nil, fmt.Errorf("--multi must be at least 1")
	}
	if multi > len(cleaned) {
		return nil, fmt.Errorf("--multi (%d) cannot exceed the number of options (%d)", multi, len(cleaned))
	}
	return cleaned, nil
}

func sendPollMessage(ctx context.Context, sender pollSender, to types.JID, question string, options []string, multi int, ephemeral bool) (types.MessageID, error) {
	return sender.SendPoll(ctx, to, question, options, multi, ephemeral)
}

func persistOutboundPoll(ctx context.Context, a *app.App, chat types.JID, msgID, question string, options []string, selectable uint32, now time.Time) {
	chatJID := primaryPollChatJID(ctx, a, chat)
	chatName := a.WA().ResolveChatName(ctx, chat, "")
	_ = a.DB().UpsertChat(chatJID, chatKindFromJID(chat), chatName, now)
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    chatJID,
		ChatName:   chatName,
		MsgID:      msgID,
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       "Poll: " + question,
	})
	_ = a.DB().UpsertPoll(store.Poll{
		ChatJID:         chatJID,
		MsgID:           msgID,
		SenderJID:       outboundPollSenderJID(ctx, a.WA(), chat),
		Question:        question,
		Options:         options,
		SelectableCount: selectable,
		CreatedAt:       now,
	})
}

func outboundPollSenderJID(ctx context.Context, wa outboundPollIdentityResolver, chat types.JID) string {
	if wa == nil {
		return ""
	}
	linked := strings.TrimSpace(wa.LinkedJID())
	if chat.Server != types.GroupServer {
		return linked
	}
	info, _ := wa.GetGroupInfo(ctx, chat)
	if info == nil || info.AddressingMode != types.AddressingModeLID {
		return linked
	}
	linkedJID, err := types.ParseJID(linked)
	if err != nil {
		return ""
	}
	lid := wa.ResolvePNToLID(ctx, linkedJID)
	if lid.IsEmpty() || lid.Server != types.HiddenUserServer {
		return ""
	}
	return lid.String()
}

func executeDelegatedPoll(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	cleaned, err := validatePollOptions(req.Question, req.Options, req.Selectable)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	msgID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return sendPollMessage(ctx, a.WA(), toJID, req.Question, cleaned, req.Selectable, req.Ephemeral)
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	persistOutboundPoll(ctx, a, toJID, string(msgID), req.Question, cleaned, uint32(req.Selectable), time.Now().UTC())
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return sendDelegateResponse{
		OK:       true,
		Sent:     true,
		To:       toJID.String(),
		ID:       string(msgID),
		Question: req.Question,
		Options:  cleaned,
	}, nil
}
