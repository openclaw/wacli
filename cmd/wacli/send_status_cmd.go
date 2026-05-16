package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type statusTextOptions struct {
	BackgroundColor string
	Font            *int32
}

type statusMessageSender interface {
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
}

func newSendStatusCmd(flags *rootFlags) *cobra.Command {
	var message string
	var backgroundColor string
	var font int32
	var fontSet bool
	var filePath string
	var mimeOverride string
	var postSendWait = postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Send a status broadcast",
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath = strings.TrimSpace(filePath)
			if strings.TrimSpace(message) == "" && filePath == "" {
				return fmt.Errorf("--message or --file is required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			if cmd.Flags().Changed("font") {
				fontSet = true
			}
			var fontPtr *int32
			if fontSet {
				fontPtr = &font
			}
			var msg *waProto.Message
			var err error
			if filePath == "" {
				msg, err = buildStatusTextMessage(message, statusTextOptions{BackgroundColor: backgroundColor, Font: fontPtr})
				if err != nil {
					return err
				}
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}

			if filePath != "" {
				msgID, meta, err := sendFile(ctx, a, types.StatusBroadcastJID, filePath, sendFileOptions{
					caption:      message,
					mimeOverride: mimeOverride,
				})
				if err != nil {
					return err
				}
				waitForPostSendRetryReceipts(ctx, postSendWait)
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"sent":      true,
						"to":        types.StatusBroadcastJID.String(),
						"id":        msgID,
						"media":     meta["media"],
						"mime_type": meta["mime_type"],
					})
				}
				fmt.Fprintf(os.Stdout, "Sent status (id %s)\n", msgID)
				return nil
			}

			msgID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return sendStatusTextMessage(ctx, a.WA(), msg)
			})
			if err != nil {
				return err
			}

			now := time.Now().UTC()
			chat := types.StatusBroadcastJID
			var storedFont int32
			if fontPtr != nil {
				storedFont = *fontPtr
			}
			_ = a.DB().UpsertStatusMessage(store.UpsertStatusMessageParams{
				MsgID:           string(msgID),
				Timestamp:       now,
				FromMe:          true,
				Text:            message,
				BackgroundColor: backgroundColor,
				Font:            storedFont,
			})

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent": true,
					"to":   chat.String(),
					"id":   msgID,
				})
			}
			fmt.Fprintf(os.Stdout, "Sent status (id %s)\n", msgID)
			return nil
		},
	}
	cmd.Flags().StringVar(&message, "message", "", "status text or media caption")
	cmd.Flags().StringVar(&filePath, "file", "", "media file to send as a status update")
	cmd.Flags().StringVar(&mimeOverride, "mime", "", "override detected MIME type for --file")
	cmd.Flags().StringVar(&backgroundColor, "background-color", "", "text status background color (#RRGGBB or #AARRGGBB)")
	cmd.Flags().Int32Var(&font, "font", 0, "WhatsApp text status font number")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

func buildStatusTextMessage(text string, opts statusTextOptions) (*waProto.Message, error) {
	ext := &waProto.ExtendedTextMessage{Text: proto.String(text)}
	if strings.TrimSpace(opts.BackgroundColor) != "" {
		color, err := parseStatusBackgroundColor(opts.BackgroundColor)
		if err != nil {
			return nil, err
		}
		ext.BackgroundArgb = proto.Uint32(color)
	}
	if opts.Font != nil {
		ext.Font = waProto.ExtendedTextMessage_FontType(*opts.Font).Enum()
	}
	return &waProto.Message{ExtendedTextMessage: ext}, nil
}

func parseStatusBackgroundColor(s string) (uint32, error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	if len(s) == 6 {
		s = "ff" + s
	}
	if len(s) != 8 {
		return 0, fmt.Errorf("--background-color must be #RRGGBB or #AARRGGBB")
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid --background-color: %w", err)
	}
	return uint32(v), nil
}

func sendStatusTextMessage(ctx context.Context, sender statusMessageSender, msg *waProto.Message) (types.MessageID, error) {
	return sender.SendProtoMessage(ctx, types.StatusBroadcastJID, msg)
}
