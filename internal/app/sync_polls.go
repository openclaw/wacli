package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handlePollSideEffects writes Poll / PollVote rows after the underlying
// message has been persisted to the messages table.
func (a *App) handlePollSideEffects(ctx context.Context, pm wa.ParsedMessage, evt *events.Message) {
	if pm.Poll != nil {
		a.upsertPollFromParsed(ctx, pm)
	}
	if pm.PollAdd != nil && evt != nil {
		a.handlePollAddOption(ctx, pm, evt)
	}
	if pm.PollVote != nil && evt != nil {
		a.handlePollVote(ctx, pm, evt)
	}
}

// handleHistoryPollSideEffects mirrors handlePollSideEffects for messages
// arriving via HistorySync, where we have a *waProto.WebMessageInfo rather
// than an events.Message. Vote decryption requires an events.Message-shaped
// input, which we reconstruct via ParseWebMessage.
func (a *App) handleHistoryPollSideEffects(ctx context.Context, pm wa.ParsedMessage, evt *events.Message, hist *waProto.WebMessageInfo) {
	if evt == nil {
		if normalized, parsed, ok := a.normalizeHistoryPollMessage(pm, hist); ok {
			pm = normalized
			evt = parsed
		}
	}
	if pm.Poll != nil {
		a.upsertPollFromParsed(ctx, pm)
	}
	if pm.PollAdd != nil {
		if evt == nil && hist != nil {
			parsed, err := a.wa.ParseWebMessage(pm.Chat, hist)
			if err != nil {
				a.emitWarning(
					"poll_add_parse_failed",
					fmt.Sprintf("warning: failed to parse history poll option %s: %v", pm.ID, err),
					map[string]any{"message_id": pm.ID, "error": err.Error()},
				)
				return
			}
			evt = parsed
		}
		if evt != nil {
			a.handlePollAddOption(ctx, pm, evt)
		}
	}
	if pm.PollVote != nil {
		if evt == nil && hist != nil {
			parsed, err := a.wa.ParseWebMessage(pm.Chat, hist)
			if err != nil {
				a.emitWarning(
					"poll_vote_parse_failed",
					fmt.Sprintf("warning: failed to parse history poll vote %s: %v", pm.ID, err),
					map[string]any{"message_id": pm.ID, "error": err.Error()},
				)
				return
			}
			evt = parsed
		}
		if evt == nil {
			a.emitWarning(
				"poll_vote_parse_failed",
				fmt.Sprintf("warning: failed to parse history poll vote %s: missing history message", pm.ID),
				map[string]any{"message_id": pm.ID, "error": "missing history message"},
			)
			return
		}
		a.handlePollVote(ctx, pm, evt)
	}
}

type historyPollSideEffect struct {
	pm   wa.ParsedMessage
	evt  *events.Message
	hist *waProto.WebMessageInfo
}

func (a *App) handleHistoryPollSideEffectsBatch(ctx context.Context, pending []historyPollSideEffect) {
	for _, item := range pending {
		if item.pm.Poll != nil {
			a.handleHistoryPollSideEffects(ctx, item.pm, item.evt, item.hist)
		}
	}
	for _, item := range pending {
		if item.pm.Poll == nil && item.pm.PollAdd != nil {
			a.handleHistoryPollSideEffects(ctx, item.pm, item.evt, item.hist)
		}
	}
	for _, item := range pending {
		if item.pm.Poll == nil && item.pm.PollAdd == nil && item.pm.PollVote != nil {
			a.handleHistoryPollSideEffects(ctx, item.pm, item.evt, item.hist)
		}
	}
}

func (a *App) normalizeHistoryPollMessage(pm wa.ParsedMessage, hist *waProto.WebMessageInfo) (wa.ParsedMessage, *events.Message, bool) {
	if hist == nil || !historyPollNeedsEventParse(pm, hist) {
		return pm, nil, false
	}
	parsed, err := a.wa.ParseWebMessage(pm.Chat, hist)
	if err != nil {
		a.emitWarning(
			"poll_history_parse_failed",
			fmt.Sprintf("warning: failed to parse history poll message %s: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
		return pm, nil, false
	}
	normalized := wa.ParseLiveMessage(parsed)
	if normalized.Poll == nil && normalized.PollAdd == nil && normalized.PollVote == nil {
		return pm, parsed, false
	}
	if pm.StarredKnown {
		normalized.StarredKnown = true
		normalized.Starred = pm.Starred
	}
	return normalized, parsed, true
}

func historyPollNeedsEventParse(pm wa.ParsedMessage, hist *waProto.WebMessageInfo) bool {
	if pm.Poll != nil || pm.PollAdd != nil || pm.PollVote != nil {
		return true
	}
	if hist == nil {
		return false
	}
	msg := hist.GetMessage()
	if msg == nil {
		return false
	}
	return msg.GetDeviceSentMessage().GetMessage() != nil ||
		msg.GetEphemeralMessage().GetMessage() != nil ||
		msg.GetViewOnceMessage().GetMessage() != nil ||
		msg.GetViewOnceMessageV2().GetMessage() != nil ||
		msg.GetViewOnceMessageV2Extension().GetMessage() != nil
}

func (a *App) handlePollAddOption(ctx context.Context, pm wa.ParsedMessage, evt *events.Message) {
	if a.db == nil || pm.PollAdd == nil {
		return
	}
	add := pm.PollAdd
	if strings.TrimSpace(add.Option) == "" && evt != nil && evt.Message.GetSecretEncryptedMessage() != nil {
		decrypted, err := a.wa.DecryptSecretEncryptedMessage(ctx, evt)
		if err != nil {
			a.emitWarning(
				"poll_add_decrypt_failed",
				fmt.Sprintf("warning: failed to decrypt poll option %s: %v", pm.ID, err),
				map[string]any{"message_id": pm.ID, "error": err.Error()},
			)
			return
		}
		parsed := wa.ParseLiveMessage(&events.Message{Info: evt.Info, Message: decrypted})
		if parsed.PollAdd != nil {
			add = mergePollAddOption(add, parsed.PollAdd)
		}
	}
	option := strings.TrimSpace(add.Option)
	if option == "" {
		return
	}
	chatJID, pollMsgID, err := resolvePollAddKey(pm, add)
	if err != nil {
		a.emitWarning(
			"poll_add_unknown_key",
			fmt.Sprintf("warning: poll option %s has invalid key: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
		return
	}
	poll, err := a.db.GetPoll(chatJID, pollMsgID)
	if err != nil {
		if alt, altErr := a.db.FindPollByMsgID(pollMsgID); altErr == nil {
			poll = alt
		} else {
			a.emitWarning(
				"poll_add_unknown_poll",
				fmt.Sprintf("warning: poll option %s references unknown poll %s/%s: %v", pm.ID, chatJID, pollMsgID, err),
				map[string]any{"message_id": pm.ID, "poll_chat_jid": chatJID, "poll_msg_id": pollMsgID, "error": err.Error()},
			)
			return
		}
	}
	for _, existing := range poll.Options {
		if existing == option {
			return
		}
	}
	poll.Options = append(poll.Options, option)
	if err := a.db.UpsertPoll(poll); err != nil {
		a.emitWarning(
			"poll_add_store_failed",
			fmt.Sprintf("warning: failed to store poll option %s: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
	}
}

func (a *App) upsertPollFromParsed(ctx context.Context, pm wa.ParsedMessage) {
	if a.db == nil || pm.Poll == nil {
		return
	}
	chatJID := canonicalJIDString(a.canonicalStoreJID(ctx, pm.Chat))
	if chatJID == "" {
		return
	}
	senderJID := strings.TrimSpace(pm.SenderJID)
	if jid, err := types.ParseJID(senderJID); err == nil && pm.Chat.Server != types.GroupServer {
		senderJID = canonicalJIDString(a.canonicalStoreJID(ctx, jid))
	} else if err == nil && pm.FromMe && jid.Server == types.DefaultUserServer {
		if info, infoErr := a.wa.GetGroupInfo(ctx, pm.Chat); infoErr == nil && info != nil && info.AddressingMode == types.AddressingModeLID {
			if lid := a.wa.ResolvePNToLID(ctx, jid); !lid.IsEmpty() && lid.Server == types.HiddenUserServer {
				senderJID = canonicalJIDString(lid)
			}
		}
	}
	if err := a.db.UpsertPoll(store.Poll{
		ChatJID:         chatJID,
		MsgID:           pm.ID,
		SenderJID:       senderJID,
		Question:        pm.Poll.Question,
		Options:         pm.Poll.Options,
		SelectableCount: pm.Poll.SelectableCount,
		CreatedAt:       pm.Timestamp,
	}); err != nil {
		a.emitWarning(
			"poll_upsert_failed",
			fmt.Sprintf("warning: failed to store poll %s: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
	}
}

func (a *App) handlePollVote(ctx context.Context, pm wa.ParsedMessage, evt *events.Message) {
	if a.db == nil || pm.PollVote == nil || evt == nil {
		return
	}
	chatJID, pollMsgID, err := resolvePollKey(pm)
	if err != nil {
		a.emitWarning(
			"poll_vote_unknown_key",
			fmt.Sprintf("warning: poll vote %s has invalid key: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
		return
	}

	poll, err := a.db.GetPoll(chatJID, pollMsgID)
	if err != nil {
		// Fall back to msg-id-only lookup. WhatsApp re-keys self-poll
		// votes under the LID rather than the phone-number JID, so the
		// (chat, id) tuple in the vote event won't match the row we
		// stored when the poll was sent.
		if alt, altErr := a.db.FindPollByMsgID(pollMsgID); altErr == nil {
			poll = alt
			chatJID = alt.ChatJID
		} else {
			a.emitWarning(
				"poll_vote_unknown_poll",
				fmt.Sprintf("warning: poll vote %s references unknown poll %s/%s: %v", pm.ID, chatJID, pollMsgID, err),
				map[string]any{
					"message_id":    pm.ID,
					"poll_chat_jid": chatJID,
					"poll_msg_id":   pollMsgID,
					"error":         err.Error(),
				},
			)
			return
		}
	}

	decrypted, err := a.wa.DecryptPollVote(ctx, evt)
	if err != nil {
		a.emitWarning(
			"poll_vote_decrypt_failed",
			fmt.Sprintf("warning: failed to decrypt poll vote %s: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
		return
	}

	selectedHashes := decrypted.GetSelectedOptions()
	selected := matchPollOptions(poll.Options, selectedHashes)

	voterJID := strings.TrimSpace(pm.SenderJID)
	if voterJID == "" && pm.FromMe {
		voterJID = a.wa.LinkedJID()
	}
	if parsed, err := types.ParseJID(voterJID); err == nil {
		voterJID = canonicalJIDString(a.wa.ResolveLIDToPN(ctx, parsed))
	}
	if voterJID == "" {
		a.emitWarning(
			"poll_vote_no_voter",
			fmt.Sprintf("warning: poll vote %s has no voter JID", pm.ID),
			map[string]any{"message_id": pm.ID},
		)
		return
	}

	votedAt := pollVoteTimestamp(pm)
	if len(selectedHashes) == 0 {
		if err := a.db.DeletePollVote(chatJID, pollMsgID, voterJID, votedAt); err != nil {
			a.emitWarning(
				"poll_vote_delete_failed",
				fmt.Sprintf("warning: failed to delete poll vote %s: %v", pm.ID, err),
				map[string]any{"message_id": pm.ID, "error": err.Error()},
			)
		}
		return
	}

	if err := a.db.UpsertPollVote(store.PollVote{
		ChatJID:   chatJID,
		PollMsgID: pollMsgID,
		VoterJID:  voterJID,
		VoteMsgID: pm.ID,
		Selected:  selected,
		VotedAt:   votedAt,
	}); err != nil {
		a.emitWarning(
			"poll_vote_store_failed",
			fmt.Sprintf("warning: failed to store poll vote %s: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
	}
}

func pollVoteTimestamp(pm wa.ParsedMessage) time.Time {
	if pm.PollVote != nil && pm.PollVote.SenderTsMS > 0 {
		return time.UnixMilli(pm.PollVote.SenderTsMS).UTC()
	}
	return pm.Timestamp
}

// resolvePollKey returns the (chat, msg_id) for the poll referenced by a
// PollUpdateMessage. The PollCreationMessageKey embeds chat (RemoteJID) and
// msg id; we trust that.
func resolvePollKey(pm wa.ParsedMessage) (string, string, error) {
	if pm.PollVote == nil {
		return "", "", fmt.Errorf("not a poll vote")
	}
	rawChat := strings.TrimSpace(pm.PollVote.PollChatJID)
	chatJID := ""
	if rawChat != "" {
		if jid, err := types.ParseJID(rawChat); err == nil {
			chatJID = canonicalJIDString(jid)
		}
	}
	if chatJID == "" {
		chatJID = canonicalJIDString(pm.Chat)
	}
	pollMsgID := strings.TrimSpace(pm.PollVote.PollMessageID)
	if chatJID == "" || pollMsgID == "" {
		return "", "", fmt.Errorf("missing poll chat or id")
	}
	return chatJID, pollMsgID, nil
}

func mergePollAddOption(wrapper, decrypted *wa.PollAddOptionRef) *wa.PollAddOptionRef {
	if wrapper == nil {
		return decrypted
	}
	if decrypted == nil {
		return wrapper
	}
	merged := *wrapper
	if strings.TrimSpace(decrypted.Option) != "" {
		merged.Option = decrypted.Option
	}
	if strings.TrimSpace(merged.PollMessageID) == "" {
		merged.PollMessageID = decrypted.PollMessageID
	}
	if strings.TrimSpace(merged.PollChatJID) == "" {
		merged.PollChatJID = decrypted.PollChatJID
	}
	if strings.TrimSpace(merged.PollSenderJID) == "" {
		merged.PollSenderJID = decrypted.PollSenderJID
	}
	return &merged
}

func resolvePollAddKey(pm wa.ParsedMessage, add *wa.PollAddOptionRef) (string, string, error) {
	if add == nil {
		return "", "", fmt.Errorf("not a poll option add")
	}
	rawChat := strings.TrimSpace(add.PollChatJID)
	chatJID := ""
	if rawChat != "" {
		if jid, err := types.ParseJID(rawChat); err == nil {
			chatJID = canonicalJIDString(jid)
		}
	}
	if chatJID == "" {
		chatJID = canonicalJIDString(pm.Chat)
	}
	pollMsgID := strings.TrimSpace(add.PollMessageID)
	if chatJID == "" || pollMsgID == "" {
		return "", "", fmt.Errorf("missing poll chat or id")
	}
	return chatJID, pollMsgID, nil
}

// matchPollOptions maps SHA-256 vote hashes back to option names by hashing
// the stored option list (whatsmeow.HashPollOptions) and matching by bytes.
// Unknown hashes are dropped silently.
func matchPollOptions(stored []string, hashes [][]byte) []string {
	if len(hashes) == 0 {
		return []string{}
	}
	storedHashes := whatsmeow.HashPollOptions(stored)
	out := make([]string, 0, len(hashes))
	for _, h := range hashes {
		for i, sh := range storedHashes {
			if bytes.Equal(h, sh) {
				out = append(out, stored[i])
				break
			}
		}
	}
	return out
}
