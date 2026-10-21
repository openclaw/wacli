package main

// Delegation of `messages delete` (including group admin deletes) and
// `groups participants <action>` to a running `sync --follow` process through
// the send-delegate socket, so moderation writes reuse its WhatsApp connection
// while it holds the store lock.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

const (
	deleteDelegateKind       = "delete"
	participantsDelegateKind = "group_participants"
)

func executeDelegatedDelete(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	msg, chatJID, err := loadMessageMutationTarget(ctx, a, req.To, req.ID)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	revokeSender, err := resolveRevokeSender(msg, true, chatJID, req.Sender)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	// This runs inside the sync process, whose stdout/stderr may carry the
	// NDJSON event stream: collect the rapid-send warning and return it to the
	// caller instead of writing it here.
	var rapid strings.Builder
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), &rapid); err != nil {
		return sendDelegateResponse{}, err
	}
	sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return a.WA().RevokeMessage(ctx, chatJID, revokeSender, types.MessageID(msg.MsgID))
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	resp := sendDelegateResponse{OK: true, Sent: true, To: chatJID.String(), ID: string(sentID), Target: msg.MsgID, SendWarning: strings.TrimSpace(rapid.String())}
	if err := a.DB().MarkMessageRevoked(msg.ChatJID, msg.MsgID); err != nil {
		resp.StoreWarning = fmt.Sprintf("store deleted message state: %v", err)
	}
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return resp, nil
}

func executeDelegatedGroupParticipants(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	gjid, jids, err := parseParticipantsRequest(req.To, req.ParticipantAction, req.Users)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	action := req.ParticipantAction
	updated, err := a.WA().UpdateGroupParticipants(ctx, gjid, jids, wa.GroupParticipantAction(action))
	if err != nil {
		return sendDelegateResponse{}, err
	}
	resp := sendDelegateResponse{OK: true, To: gjid.String(), Action: action}
	info, err := a.WA().GetGroupInfo(ctx, gjid)
	if err == nil && info != nil {
		err = persistGroupInfo(ctx, a.DB(), a.WA(), info)
	}
	if err != nil {
		resp.StoreWarning = fmt.Sprintf("refresh stored group info: %v", err)
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	resp.Participants = raw
	return resp, nil
}

// parseParticipantsRequest strictly validates a participant change before it
// is delegated and again inside the sync process, so malformed input never
// reaches WhatsApp.
func parseParticipantsRequest(group, action string, users []string) (types.JID, []types.JID, error) {
	switch action {
	case "add", "remove", "promote", "demote":
	default:
		return types.EmptyJID, nil, fmt.Errorf("unknown participant action %q (want add, remove, promote, or demote)", sanitize(action))
	}
	if len(users) == 0 {
		return types.EmptyJID, nil, fmt.Errorf("at least one --user is required")
	}
	gjid, err := types.ParseJID(strings.TrimSpace(group))
	if err != nil || gjid.User == "" || gjid.Server != types.GroupServer {
		return types.EmptyJID, nil, fmt.Errorf("invalid group %q: pass a group JID ending in @%s (see `wacli groups list`)", sanitize(group), types.GroupServer)
	}
	jids := make([]types.JID, 0, len(users))
	for _, u := range users {
		j, err := wa.ParseUserOrJID(u)
		if err == nil {
			err = validateParticipantJID(j)
		}
		if err != nil {
			return types.EmptyJID, nil, fmt.Errorf("invalid user %q: %s; pass a phone number, @%s or @%s JID", sanitize(u), sanitize(err.Error()), types.DefaultUserServer, types.HiddenUserServer)
		}
		jids = append(jids, j)
	}
	return gjid, jids, nil
}

// validateParticipantJID rejects malformed user JIDs locally so they never reach WhatsApp.
func validateParticipantJID(j types.JID) error {
	if strings.TrimSpace(j.User) == "" {
		return fmt.Errorf("empty user part")
	}
	if j.Server != types.DefaultUserServer && j.Server != types.HiddenUserServer {
		return fmt.Errorf("unsupported server %q", j.Server)
	}
	return nil
}

// warnDelegatedModeration prints warnings the sync process returned to the
// caller's stderr; the daemon itself never writes them.
func warnDelegatedModeration(w io.Writer, resp sendDelegateResponse) {
	if resp.SendWarning != "" {
		fmt.Fprintln(w, sanitize(resp.SendWarning))
	}
	if resp.StoreWarning != "" {
		fmt.Fprintf(w, "warning: change applied but local store update failed: %s\n", sanitize(resp.StoreWarning))
	}
}
