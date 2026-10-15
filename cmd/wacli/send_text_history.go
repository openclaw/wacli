package main

import (
	"context"
	"time"

	"github.com/openclaw/wacli/internal/linkpreview"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// sendTextWithHistory is shared by direct and socket-delegated sends. Persist
// only after acceptance, using the actual native context from the final attempt.
// A local save failure remains a warning and must never retry an accepted send.
func sendTextWithHistory(ctx context.Context, a sendTextApp, reconnect func(context.Context) error, to types.JID, text, replyTo, replyToSender string, preview *linkpreview.Preview, mentions []string, ephemeral textEphemeralOptions, options textSendOptions) (types.MessageID, error, error) {
	var replyContext *waProto.ContextInfo
	options.replyContext = &replyContext
	id, err := runSendOperation(ctx, reconnect, func(ctx context.Context) (types.MessageID, error) {
		replyContext = nil
		return sendTextMessage(ctx, a, to, text, replyTo, replyToSender, preview, mentions, ephemeral, options)
	})
	if err != nil {
		return "", nil, err
	}
	storeErr := persistOutboundTextWith(ctx, a.DB(), a.WA(), to, string(id), text, time.Now().UTC(), replyContext)
	return id, storeErr, nil
}
