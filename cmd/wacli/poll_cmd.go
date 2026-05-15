package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newPollCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "poll",
		Short: "Vote on or inspect a poll",
	}
	cmd.AddCommand(newPollVoteCmd(flags))
	cmd.AddCommand(newPollShowCmd(flags))
	return cmd
}

func newPollsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "polls",
		Short: "List polls",
	}
	cmd.AddCommand(newPollsListCmd(flags))
	return cmd
}

// ---- vote -----------------------------------------------------------------

type pollVoteSender interface {
	SendPollVote(ctx context.Context, pollInfo *types.MessageInfo, options []string) (types.MessageID, error)
}

func newPollVoteCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var msgID string
	var voteOptions []string
	var senderOverride string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "vote",
		Short: "Vote on a poll",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" || strings.TrimSpace(msgID) == "" {
				return fmt.Errorf("--to and --id are required")
			}
			cleaned, err := cleanVoteOptions(voteOptions)
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
					Kind:           "poll_vote",
					To:             to,
					Pick:           pick,
					ID:             msgID,
					Sender:         senderOverride,
					Options:        cleaned,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "poll_vote", resp)
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

			info, knownOptions, selectableCount, pollChatJID, err := buildPollVoteInfo(ctx, a, toJID, msgID, senderOverride)
			if err != nil {
				return err
			}
			if knownOptions != nil {
				if err := requirePollOptionsExist(knownOptions, cleaned); err != nil {
					return err
				}
				if err := requirePollSelectableCount(selectableCount, cleaned); err != nil {
					return err
				}
			}
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}

			sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return a.WA().SendPollVote(ctx, info, cleaned)
			})
			if err != nil {
				return err
			}

			now := time.Now().UTC()
			persistOutboundVote(ctx, a, toJID, pollChatJID, msgID, string(sentID), cleaned, now)
			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent":     true,
					"to":       toJID.String(),
					"id":       string(sentID),
					"target":   msgID,
					"selected": cleaned,
				})
			}
			fmt.Fprintf(os.Stdout, "Voted on %s in %s (id %s)\n", msgID, toJID.String(), sentID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "chat JID, phone number, or contact/group/chat name where the poll lives")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&msgID, "id", "", "poll message ID (the original PollCreationMessage)")
	cmd.Flags().StringArrayVar(&voteOptions, "option", nil, "option to vote for (repeat for multi-select)")
	cmd.Flags().StringVar(&senderOverride, "sender", "", "JID of the poll author (required for groups when the poll is not in the local store)")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

func cleanVoteOptions(opts []string) ([]string, error) {
	cleaned := make([]string, 0, len(opts))
	seen := make(map[string]struct{}, len(opts))
	for _, opt := range opts {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		if _, dup := seen[opt]; dup {
			continue
		}
		seen[opt] = struct{}{}
		cleaned = append(cleaned, opt)
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("at least one --option is required (use --option \"\" to retract a vote is not supported)")
	}
	return cleaned, nil
}

func requirePollOptionsExist(known, requested []string) error {
	set := make(map[string]struct{}, len(known))
	for _, k := range known {
		set[k] = struct{}{}
	}
	for _, opt := range requested {
		if _, ok := set[opt]; !ok {
			return fmt.Errorf("option %q is not part of the poll (known: %s)", opt, strings.Join(known, ", "))
		}
	}
	return nil
}

func requirePollSelectableCount(selectable uint32, requested []string) error {
	if selectable == 0 || len(requested) <= int(selectable) {
		return nil
	}
	return fmt.Errorf("poll allows at most %d option(s); got %d", selectable, len(requested))
}

// buildPollVoteInfo resolves the MessageInfo whatsmeow needs to encrypt the
// vote. Looks up the poll in the local store first; falls back to manually
// supplied --sender for unknown polls.
func buildPollVoteInfo(ctx context.Context, a *app.App, chat types.JID, pollMsgID, senderOverride string) (*types.MessageInfo, []string, uint32, string, error) {
	return buildPollVoteInfoForChats(ctx, a, chat, pollChatJIDCandidates(ctx, a, chat), pollMsgID, senderOverride)
}

func buildPollVoteInfoForChats(ctx context.Context, a *app.App, chat types.JID, chatJIDs []string, pollMsgID, senderOverride string) (*types.MessageInfo, []string, uint32, string, error) {
	chatJID := ""
	if len(chatJIDs) > 0 {
		chatJID = chatJIDs[0]
	}
	if chatJID == "" {
		chatJID = canonicalMessageFilterJID(chat).String()
	}

	var (
		senderJID       types.JID
		knownOptions    []string
		selectableCount uint32
		ts              time.Time
	)
	pollSenderRaw := strings.TrimSpace(senderOverride)
	if pollSenderRaw != "" {
		jid, err := wa.ParseUserOrJID(pollSenderRaw)
		if err != nil {
			return nil, nil, 0, "", fmt.Errorf("invalid --sender: %w", err)
		}
		senderJID = jid
	}

	fromMe := false
	var poll store.Poll
	pollFound := false
	for _, candidate := range chatJIDs {
		p, err := a.DB().GetPoll(candidate, pollMsgID)
		switch {
		case err == nil:
			poll = p
			pollFound = true
			chatJID = poll.ChatJID
		case errors.Is(err, sql.ErrNoRows):
			continue
		default:
			return nil, nil, 0, "", fmt.Errorf("lookup poll: %w", err)
		}
		if pollFound {
			break
		}
	}
	if pollFound {
		knownOptions = append([]string(nil), poll.Options...)
		selectableCount = poll.SelectableCount
		if senderJID.IsEmpty() && strings.TrimSpace(poll.SenderJID) != "" {
			if jid, perr := types.ParseJID(poll.SenderJID); perr == nil {
				senderJID = jid
			}
		}
		ts = poll.CreatedAt
	}

	// Look up the original poll message to recover IsFromMe (and fall back
	// for sender / timestamp if the poll row didn't carry them).
	if msg, merr := a.DB().GetMessage(chatJID, pollMsgID); merr == nil {
		fromMe = msg.FromMe
		if senderJID.IsEmpty() && strings.TrimSpace(msg.SenderJID) != "" {
			if jid, perr := types.ParseJID(msg.SenderJID); perr == nil {
				senderJID = jid
			}
		}
		if ts.IsZero() {
			ts = msg.Timestamp
		}
	} else if !errors.Is(merr, sql.ErrNoRows) {
		return nil, nil, 0, "", fmt.Errorf("lookup poll message: %w", merr)
	}

	// If we sent the poll, the encrypted secret was stored against our own
	// JID rather than against the recipient. Use the linked JID as sender
	// so BuildPollVote can find the message secret and set the right key.
	if fromMe && senderJID.IsEmpty() {
		if linked := strings.TrimSpace(a.WA().LinkedJID()); linked != "" {
			if jid, perr := types.ParseJID(linked); perr == nil {
				senderJID = jid
			}
		}
	}

	if senderJID.IsEmpty() {
		// 1:1 DMs: the chat itself is the sender unless we sent the poll.
		if chat.Server == types.DefaultUserServer || chat.Server == types.HiddenUserServer {
			senderJID = chat
		} else if chat.Server == types.GroupServer {
			return nil, knownOptions, selectableCount, "", fmt.Errorf("poll %s is not in the local store; pass --sender <JID> to vote", pollMsgID)
		}
	}

	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   senderJID,
			IsFromMe: fromMe,
			IsGroup:  chat.Server == types.GroupServer,
		},
		ID:        pollMsgID,
		Timestamp: ts,
	}
	return info, knownOptions, selectableCount, chatJID, nil
}

func primaryPollChatJID(ctx context.Context, a *app.App, jid types.JID) string {
	candidates := pollChatJIDCandidates(ctx, a, jid)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

func pollChatJIDCandidates(ctx context.Context, a *app.App, jid types.JID) []string {
	jids := make([]types.JID, 0, 3)
	if a != nil {
		if a.WA() == nil {
			if _, err := os.Stat(filepath.Join(a.StoreDir(), "session.db")); err == nil {
				_ = a.OpenWA()
			}
		}
		if client := a.WA(); client != nil {
			resolved := client.ResolveLIDToPN(ctx, jid)
			jids = append(jids, canonicalMessageFilterJID(resolved))
			if resolved.Server == types.DefaultUserServer {
				jids = append(jids, canonicalMessageFilterJID(client.ResolvePNToLID(ctx, resolved)))
			}
		}
	}
	jids = append(jids, canonicalMessageFilterJID(jid))
	return jidStrings(jids)
}

func persistOutboundVote(ctx context.Context, a *app.App, chat types.JID, chatJID, pollMsgID, voteMsgID string, options []string, now time.Time) {
	if strings.TrimSpace(chatJID) == "" {
		chatJID = primaryPollChatJID(ctx, a, chat)
	}
	chatName := a.WA().ResolveChatName(ctx, chat, "")
	_ = a.DB().UpsertChat(chatJID, chatKindFromJID(chat), chatName, now)
	_ = a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    chatJID,
		ChatName:   chatName,
		MsgID:      voteMsgID,
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       "Voted: " + strings.Join(options, ", "),
	})
	voter := a.WA().LinkedJID()
	if voter == "" {
		return
	}
	_ = a.DB().UpsertPollVote(store.PollVote{
		ChatJID:   chatJID,
		PollMsgID: pollMsgID,
		VoterJID:  voter,
		VoteMsgID: voteMsgID,
		Selected:  options,
		VotedAt:   now,
	})
}

func executeDelegatedPollVote(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	if strings.TrimSpace(req.To) == "" || strings.TrimSpace(req.ID) == "" {
		return sendDelegateResponse{}, fmt.Errorf("poll vote requires --to and --id")
	}
	cleaned, err := cleanVoteOptions(req.Options)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	info, knownOptions, selectableCount, pollChatJID, err := buildPollVoteInfo(ctx, a, toJID, req.ID, req.Sender)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if knownOptions != nil {
		if err := requirePollOptionsExist(knownOptions, cleaned); err != nil {
			return sendDelegateResponse{}, err
		}
		if err := requirePollSelectableCount(selectableCount, cleaned); err != nil {
			return sendDelegateResponse{}, err
		}
	}
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return a.WA().SendPollVote(ctx, info, cleaned)
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	now := time.Now().UTC()
	persistOutboundVote(ctx, a, toJID, pollChatJID, req.ID, string(sentID), cleaned, now)
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return sendDelegateResponse{
		OK:       true,
		Sent:     true,
		To:       toJID.String(),
		ID:       string(sentID),
		Target:   req.ID,
		Selected: cleaned,
	}, nil
}

// ---- show -----------------------------------------------------------------

func newPollShowCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var msgID string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show a poll's question, options, and votes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" || strings.TrimSpace(msgID) == "" {
				return fmt.Errorf("--to and --id are required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			toJID, err := resolveRecipient(a, to, recipientOptions{pick: pick, asJSON: flags.asJSON})
			if err != nil {
				return err
			}
			poll, votes, err := getPollForShow(ctx, a, toJID, msgID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("poll %s not found in local store for chat %s; run `wacli sync` first", msgID, toJID.String())
				}
				return err
			}

			aggregates := aggregatePollVotes(poll.Options, votes)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, pollShowPayload(a, ctx, poll, votes, aggregates))
			}
			renderPollShow(os.Stdout, a, ctx, poll, votes, aggregates)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "chat JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&msgID, "id", "", "poll message ID")
	return cmd
}

func getPollForShow(ctx context.Context, a *app.App, chat types.JID, msgID string) (store.Poll, []store.PollVote, error) {
	var lastErr error = sql.ErrNoRows
	for _, chatJID := range pollChatJIDCandidates(ctx, a, chat) {
		poll, err := a.DB().GetPoll(chatJID, msgID)
		if err != nil {
			lastErr = err
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return store.Poll{}, nil, err
		}
		votes, err := a.DB().ListPollVotes(poll.ChatJID, msgID)
		if err != nil {
			return store.Poll{}, nil, err
		}
		return poll, votes, nil
	}
	return store.Poll{}, nil, lastErr
}

func aggregatePollVotes(options []string, votes []store.PollVote) map[string]int {
	out := make(map[string]int, len(options))
	for _, o := range options {
		out[o] = 0
	}
	for _, v := range votes {
		for _, sel := range v.Selected {
			out[sel]++
		}
	}
	return out
}

type pollVoterPayload struct {
	JID      string   `json:"jid"`
	Name     string   `json:"name,omitempty"`
	Selected []string `json:"selected"`
	VotedAt  string   `json:"voted_at"`
}

type pollShowJSON struct {
	Question        string             `json:"question"`
	Options         []string           `json:"options"`
	SelectableCount uint32             `json:"selectable_count"`
	ChatJID         string             `json:"chat_jid"`
	MsgID           string             `json:"msg_id"`
	SenderJID       string             `json:"sender_jid,omitempty"`
	CreatedAt       string             `json:"created_at"`
	Aggregates      map[string]int     `json:"aggregates"`
	Voters          []pollVoterPayload `json:"voters"`
}

func pollShowPayload(a *app.App, ctx context.Context, poll store.Poll, votes []store.PollVote, aggregates map[string]int) pollShowJSON {
	out := pollShowJSON{
		Question:        poll.Question,
		Options:         poll.Options,
		SelectableCount: poll.SelectableCount,
		ChatJID:         poll.ChatJID,
		MsgID:           poll.MsgID,
		SenderJID:       poll.SenderJID,
		CreatedAt:       poll.CreatedAt.Format(time.RFC3339),
		Aggregates:      aggregates,
		Voters:          make([]pollVoterPayload, 0, len(votes)),
	}
	for _, v := range votes {
		name := resolveVoterName(a, ctx, v.VoterJID)
		out.Voters = append(out.Voters, pollVoterPayload{
			JID:      v.VoterJID,
			Name:     name,
			Selected: v.Selected,
			VotedAt:  v.VotedAt.Format(time.RFC3339),
		})
	}
	return out
}

func resolveVoterName(a *app.App, ctx context.Context, jid string) string {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return ""
	}
	if a != nil && a.DB() != nil {
		if c, err := a.DB().GetContact(jid); err == nil {
			if name := strings.TrimSpace(c.Name); name != "" {
				return name
			}
		}
	}
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return ""
	}
	if a == nil || a.WA() == nil {
		return ""
	}
	return a.WA().ResolveChatName(ctx, parsed, "")
}

func renderPollShow(w *os.File, a *app.App, ctx context.Context, poll store.Poll, votes []store.PollVote, aggregates map[string]int) {
	fmt.Fprintf(w, "Poll: %s\n", poll.Question)
	fmt.Fprintf(w, "Chat: %s   Msg: %s   Selectable: %d\n", poll.ChatJID, poll.MsgID, poll.SelectableCount)
	fmt.Fprintf(w, "\nResults (%d voter(s)):\n", len(votes))
	for _, opt := range poll.Options {
		fmt.Fprintf(w, "  %3d  %s\n", aggregates[opt], opt)
	}
	if len(votes) == 0 {
		fmt.Fprintln(w, "\nNo votes yet.")
		return
	}
	fmt.Fprintln(w, "\nVoters:")
	for _, v := range votes {
		name := resolveVoterName(a, ctx, v.VoterJID)
		who := v.VoterJID
		if name != "" && name != v.VoterJID {
			who = fmt.Sprintf("%s (%s)", name, v.VoterJID)
		}
		fmt.Fprintf(w, "  %s — %s [%s]\n", who, strings.Join(v.Selected, ", "), v.VotedAt.Format(time.RFC3339))
	}
}

// ---- list -----------------------------------------------------------------

func newPollsListCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var pick int
	var limit int

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List polls stored locally",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			filter := store.PollListFilter{Limit: limit}
			if strings.TrimSpace(chat) != "" {
				toJID, err := resolveRecipient(a, chat, recipientOptions{pick: pick, asJSON: flags.asJSON})
				if err != nil {
					return err
				}
				filter.ChatJIDs = pollChatJIDCandidates(ctx, a, toJID)
			}
			polls, err := a.DB().ListPolls(filter)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"polls": pollsToJSON(polls)})
			}
			renderPollsList(os.Stdout, polls)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "filter to a single chat")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --chat is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max polls to return (0 = unlimited)")
	return cmd
}

type pollListItemJSON struct {
	ChatJID         string   `json:"chat_jid"`
	MsgID           string   `json:"msg_id"`
	Question        string   `json:"question"`
	Options         []string `json:"options"`
	SelectableCount uint32   `json:"selectable_count"`
	SenderJID       string   `json:"sender_jid,omitempty"`
	CreatedAt       string   `json:"created_at"`
}

func pollsToJSON(polls []store.Poll) []pollListItemJSON {
	out := make([]pollListItemJSON, 0, len(polls))
	for _, p := range polls {
		out = append(out, pollListItemJSON{
			ChatJID:         p.ChatJID,
			MsgID:           p.MsgID,
			Question:        p.Question,
			Options:         p.Options,
			SelectableCount: p.SelectableCount,
			SenderJID:       p.SenderJID,
			CreatedAt:       p.CreatedAt.Format(time.RFC3339),
		})
	}
	return out
}

func renderPollsList(w *os.File, polls []store.Poll) {
	if len(polls) == 0 {
		fmt.Fprintln(w, "No polls.")
		return
	}
	for _, p := range polls {
		fmt.Fprintf(w, "%s  %s\n  chat=%s  options=%d  selectable=%d  id=%s\n",
			p.CreatedAt.Format(time.RFC3339),
			p.Question,
			p.ChatJID,
			len(p.Options),
			p.SelectableCount,
			p.MsgID,
		)
	}
}
