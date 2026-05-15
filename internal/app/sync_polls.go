package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"

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
		a.upsertPollFromParsed(pm)
	}
	if pm.PollVote != nil && evt != nil {
		a.handlePollVote(ctx, pm, evt)
	}
}

// handleHistoryPollSideEffects mirrors handlePollSideEffects for messages
// arriving via HistorySync, where we have a *waProto.WebMessageInfo rather
// than an events.Message. Vote decryption requires an events.Message-shaped
// input, which we reconstruct via ParseWebMessage.
func (a *App) handleHistoryPollSideEffects(ctx context.Context, pm wa.ParsedMessage, hist *waProto.WebMessageInfo) {
	if pm.Poll != nil {
		a.upsertPollFromParsed(pm)
	}
	if pm.PollVote != nil && hist != nil {
		evt, err := a.wa.ParseWebMessage(pm.Chat, hist)
		if err != nil {
			a.emitWarning(
				"poll_vote_parse_failed",
				fmt.Sprintf("warning: failed to parse history poll vote %s: %v", pm.ID, err),
				map[string]any{"message_id": pm.ID, "error": err.Error()},
			)
			return
		}
		a.handlePollVote(ctx, pm, evt)
	}
}

func (a *App) upsertPollFromParsed(pm wa.ParsedMessage) {
	if a.db == nil || pm.Poll == nil {
		return
	}
	chatJID := canonicalJIDString(pm.Chat)
	if chatJID == "" {
		return
	}
	if err := a.db.UpsertPoll(store.Poll{
		ChatJID:         chatJID,
		MsgID:           pm.ID,
		SenderJID:       pm.SenderJID,
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

	selected := matchPollOptions(poll.Options, decrypted.GetSelectedOptions())

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

	if err := a.db.UpsertPollVote(store.PollVote{
		ChatJID:   chatJID,
		PollMsgID: pollMsgID,
		VoterJID:  voterJID,
		VoteMsgID: pm.ID,
		Selected:  selected,
		VotedAt:   pm.Timestamp,
	}); err != nil {
		a.emitWarning(
			"poll_vote_store_failed",
			fmt.Sprintf("warning: failed to store poll vote %s: %v", pm.ID, err),
			map[string]any{"message_id": pm.ID, "error": err.Error()},
		)
	}
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
