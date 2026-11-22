package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
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

const sendStatusKind = "status"

func newSendStatusCmd(flags *rootFlags) *cobra.Command {
	var message, backgroundColor, filePath, mimeOverride string
	var font int32
	postSendWait := postSendRetryReceiptWait
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Send a status broadcast",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			req := sendDelegateRequest{
				Kind: sendStatusKind, Message: message, File: strings.TrimSpace(filePath),
				MIME: mimeOverride, StatusBackground: backgroundColor,
				PostSendWaitMS: durationMillis(postSendWait),
			}
			if cmd.Flags().Changed("font") {
				req.StatusFont = &font
			}
			if err := validateStatusRequest(req); err != nil {
				return err
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				if req.File != "" {
					absolute, absErr := filepath.Abs(req.File)
					if absErr != nil {
						return absErr
					}
					req.File = absolute
				}
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, req)
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeStatusSendOutput(flags, resp)
				}
				return err
			}
			defer closeApp(a, lk)
			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			resp, err := executeDelegatedStatus(ctx, a, req)
			if err != nil {
				return err
			}
			return writeStatusSendOutput(flags, resp)
		},
	}
	cmd.Flags().StringVar(&message, "message", "", "status text or media caption")
	cmd.Flags().StringVar(&filePath, "file", "", "media file to send as a status update")
	cmd.Flags().StringVar(&mimeOverride, "mime", "", "override detected MIME type for --file")
	// Status rows live outside chats and never acknowledge chat unread state.
	cmd.Flags().Bool("keep-unread", false, "preserve chat unread state (status broadcasts never mark chats read)")
	cmd.Flags().StringVar(&backgroundColor, "background-color", "", "text status background color (#RRGGBB or #AARRGGBB)")
	cmd.Flags().Int32Var(&font, "font", 0, "WhatsApp text status font number")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

func validateStatusRequest(req sendDelegateRequest) error {
	if strings.TrimSpace(req.Message) == "" && req.File == "" {
		return fmt.Errorf("--message or --file is required")
	}
	if req.File != "" {
		return checkOutboundMediaPath(req.File)
	}
	_, err := buildStatusTextMessage(req.Message, statusTextOptions{BackgroundColor: req.StatusBackground, Font: req.StatusFont})
	return err
}

func executeDelegatedStatus(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	if err := validateStatusRequest(req); err != nil {
		return sendDelegateResponse{}, err
	}
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	operation := func(ctx context.Context) (sendDelegateResponse, error) { return sendStatus(ctx, a, req) }
	var resp sendDelegateResponse
	var err error
	if req.File != "" {
		resp, err = runFileSendOperation(ctx, reconnectForSend(a), req.File, operation)
	} else {
		resp, err = runSendOperation(ctx, reconnectForSend(a), operation)
	}
	if err != nil {
		return sendDelegateResponse{}, err
	}
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return resp, nil
}

type statusSendApp interface {
	WA() app.WAClient
	DB() *store.DB
}

func sendStatus(ctx context.Context, a statusSendApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	if err := validateStatusRequest(req); err != nil {
		return sendDelegateResponse{}, err
	}
	resp := sendDelegateResponse{OK: true, Sent: true, To: types.StatusBroadcastJID.String()}
	if req.File != "" {
		res, err := sendFile(ctx, a, types.StatusBroadcastJID, req.File, sendFileOptions{caption: req.Message, mimeOverride: req.MIME})
		if err != nil {
			return sendDelegateResponse{}, err
		}
		resp.ID, resp.File = res.id, res.meta
		if res.storeWarning != nil {
			resp.StoreWarning = res.storeWarning.Error()
		}
		return resp, nil
	}
	msg, err := buildStatusTextMessage(req.Message, statusTextOptions{BackgroundColor: req.StatusBackground, Font: req.StatusFont})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	id, err := sendStatusTextMessage(ctx, a.WA(), msg)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	resp.ID = string(id)
	var font int32
	if req.StatusFont != nil {
		font = *req.StatusFont
	}
	if err := a.DB().UpsertStatusMessage(store.UpsertStatusMessageParams{
		MsgID: string(id), Timestamp: time.Now().UTC(), FromMe: true,
		Text: req.Message, BackgroundColor: req.StatusBackground, Font: font,
	}); err != nil {
		resp.StoreWarning = err.Error()
	}
	return resp, nil
}

func writeStatusSendOutput(flags *rootFlags, resp sendDelegateResponse) error {
	warnSendStoreFailureMsg(os.Stderr, resp.ID, resp.StoreWarning)
	if flags.asJSON {
		body := map[string]any{"sent": resp.Sent, "to": resp.To, "id": resp.ID}
		if resp.File != nil {
			body["media"], body["mime_type"] = resp.File["media"], resp.File["mime_type"]
		}
		if resp.StoreWarning != "" {
			body["store_warning"] = resp.StoreWarning
		}
		return out.WriteJSON(os.Stdout, body)
	}
	fmt.Fprintf(os.Stdout, "Sent status (id %s)\n", resp.ID)
	return nil
}

func buildStatusTextMessage(text string, opts statusTextOptions) (*waProto.Message, error) {
	ext := &waProto.ExtendedTextMessage{Text: proto.String(text)}
	if strings.TrimSpace(opts.BackgroundColor) != "" {
		color, err := parseStatusBackground(opts.BackgroundColor)
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

func parseStatusBackground(s string) (uint32, error) {
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
