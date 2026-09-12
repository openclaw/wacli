package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func isSecretEdit(msg *waE2E.Message) bool {
	return msg != nil &&
		msg.GetSecretEncryptedMessage().GetSecretEncType() == waE2E.SecretEncryptedMessage_MESSAGE_EDIT
}

func (a *App) sameCanonicalIdentity(ctx context.Context, left, right types.JID) bool {
	left = a.canonicalStoreJID(ctx, left).ToNonAD()
	right = a.canonicalStoreJID(ctx, right).ToNonAD()
	return !left.IsEmpty() && left == right
}

func (a *App) secretEditSenderMatches(ctx context.Context, evt *events.Message, target *waCommon.MessageKey) bool {
	if evt.Info.Sender.IsEmpty() {
		return false
	}
	// whatsmeow treats target.FromMe as "same sender as the envelope",
	// not necessarily as the locally linked account. Decryption has already
	// required a matching sender (or LID/PN alias) in the SDK secret store.
	if target.GetFromMe() {
		return true
	}

	originalSenderRaw := target.GetParticipant()
	if evt.Info.Chat.Server == types.DefaultUserServer || evt.Info.Chat.Server == types.HiddenUserServer {
		originalSenderRaw = target.GetRemoteJID()
	}
	originalSender, err := types.ParseJID(strings.TrimSpace(originalSenderRaw))
	if err != nil || originalSender.IsEmpty() {
		return false
	}

	return a.sameCanonicalIdentity(ctx, evt.Info.Sender, originalSender)
}

func (a *App) secretEditChatMatches(ctx context.Context, evt *events.Message, target *waCommon.MessageKey) bool {
	targetChatRaw := strings.TrimSpace(target.GetRemoteJID())
	if targetChatRaw == "" {
		return true
	}
	targetChat, err := types.ParseJID(targetChatRaw)
	if err != nil || targetChat.IsEmpty() {
		return false
	}
	return a.sameCanonicalIdentity(ctx, evt.Info.Chat, targetChat)
}

func normalizedSecretEditTarget(evt *events.Message, targetID string) *waCommon.MessageKey {
	chat := evt.Info.Chat.ToNonAD().String()
	fromMe := evt.Info.IsFromMe
	target := &waCommon.MessageKey{
		ID:        &targetID,
		RemoteJID: &chat,
		FromMe:    &fromMe,
	}
	if evt.Info.Chat.Server != types.DefaultUserServer && evt.Info.Chat.Server != types.HiddenUserServer {
		participant := evt.Info.Sender.ToNonAD().String()
		target.Participant = &participant
	}
	return target
}

func containsNestedProtocolMutation(msg *waE2E.Message) bool {
	if msg == nil {
		return false
	}
	if msg.GetProtocolMessage() != nil {
		return true
	}
	return containsNestedProtocolMutation(msg.GetAssociatedChildMessage().GetMessage()) ||
		containsNestedProtocolMutation(msg.GetGroupStatusMentionMessage().GetMessage()) ||
		containsNestedProtocolMutation(msg.GetDeviceSentMessage().GetMessage()) ||
		containsNestedProtocolMutation(msg.GetEditedMessage().GetMessage()) ||
		containsNestedProtocolMutation(msg.GetCommentMessage().GetMessage())
}

func (a *App) decryptSecretEdit(ctx context.Context, evt *events.Message) (*events.Message, bool) {
	if evt == nil || !isSecretEdit(evt.Message) {
		return evt, true
	}
	messageID := evt.Info.ID
	secret := evt.Message.GetSecretEncryptedMessage()
	target := secret.GetTargetMessageKey()
	if strings.TrimSpace(target.GetID()) == "" {
		a.emitWarning(
			"encrypted_edit_invalid_target",
			fmt.Sprintf("warning: encrypted edit %s has no target message ID", messageID),
			map[string]any{"message_id": messageID},
		)
		return nil, false
	}
	decrypted, err := a.wa.DecryptSecretEncryptedMessage(ctx, evt)
	if err != nil {
		a.emitWarning(
			"encrypted_edit_decrypt_failed",
			fmt.Sprintf("warning: failed to decrypt message edit %s: %v", messageID, err),
			map[string]any{"message_id": messageID, "error": err.Error()},
		)
		return nil, false
	}
	protocol := decrypted.GetProtocolMessage()
	if protocol.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT || protocol.GetEditedMessage() == nil {
		a.emitWarning(
			"encrypted_edit_invalid_payload",
			fmt.Sprintf("warning: encrypted edit %s decrypted to an unexpected payload", messageID),
			map[string]any{"message_id": messageID},
		)
		return nil, false
	}
	if decryptedTarget := strings.TrimSpace(protocol.GetKey().GetID()); decryptedTarget != "" && decryptedTarget != target.GetID() {
		a.emitWarning(
			"encrypted_edit_target_mismatch",
			fmt.Sprintf("warning: encrypted edit %s target does not match decrypted payload", messageID),
			map[string]any{"message_id": messageID},
		)
		return nil, false
	}
	if !a.secretEditChatMatches(ctx, evt, target) {
		a.emitWarning(
			"encrypted_edit_chat_mismatch",
			fmt.Sprintf("warning: encrypted edit %s target chat does not match the authenticated chat", messageID),
			map[string]any{"message_id": messageID},
		)
		return nil, false
	}
	if !a.secretEditSenderMatches(ctx, evt, target) {
		a.emitWarning(
			"encrypted_edit_sender_mismatch",
			fmt.Sprintf("warning: encrypted edit %s sender does not own the target message", messageID),
			map[string]any{"message_id": messageID},
		)
		return nil, false
	}
	if containsNestedProtocolMutation(protocol.GetEditedMessage()) {
		a.emitWarning(
			"encrypted_edit_nested_mutation",
			fmt.Sprintf("warning: encrypted edit %s contains a nested protocol mutation", messageID),
			map[string]any{"message_id": messageID},
		)
		return nil, false
	}
	protocol.Key = normalizedSecretEditTarget(evt, target.GetID())
	parsed := wa.ParseLiveMessage(&events.Message{Info: evt.Info, Message: decrypted})
	if !parsed.HasContent() {
		payload := parsed.UnhandledPayload
		if payload == "" {
			payload = "empty message"
		}
		a.emitWarning(
			"encrypted_edit_unhandled_payload",
			fmt.Sprintf("warning: encrypted edit %s contains unsupported payload %s", messageID, payload),
			map[string]any{"message_id": messageID, "payload": payload},
		)
		return nil, false
	}
	parsedSender, err := types.ParseJID(strings.TrimSpace(parsed.SenderJID))
	if err != nil || !parsed.Edited || parsed.Revoked || parsed.ID != target.GetID() || parsed.FromMe != evt.Info.IsFromMe ||
		!a.sameCanonicalIdentity(ctx, evt.Info.Chat, parsed.Chat) ||
		!a.sameCanonicalIdentity(ctx, evt.Info.Sender, parsedSender) {
		a.emitWarning(
			"encrypted_edit_final_target_mismatch",
			fmt.Sprintf("warning: encrypted edit %s changed identity while parsing", messageID),
			map[string]any{"message_id": messageID},
		)
		return nil, false
	}
	return &events.Message{Info: evt.Info, Message: decrypted}, true
}
