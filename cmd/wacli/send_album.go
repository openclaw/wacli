package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	minAlbumFiles = 2
	maxAlbumFiles = 30
)

// sendAlbumOutcome is the result of an album send: the AlbumMessage parent ID
// plus one ID and metadata entry per media message, in send order.
type sendAlbumOutcome struct {
	id           string
	ids          []string
	files        []map[string]string
	storeWarning error
}

type albumSendApp interface {
	WA() app.WAClient
	DB() *store.DB
}

// albumMediaCounts validates every file before anything is sent and returns
// the image and video counts the AlbumMessage announces.
func albumMediaCounts(files []string) (uint32, uint32, error) {
	if len(files) < minAlbumFiles {
		return 0, 0, fmt.Errorf("an album needs at least %d files; use send file for one", minAlbumFiles)
	}
	if len(files) > maxAlbumFiles {
		return 0, 0, fmt.Errorf("an album takes at most %d files", maxAlbumFiles)
	}
	var images, videos uint32
	for _, f := range files {
		data, err := readSendFileData(f)
		if err != nil {
			return 0, 0, err
		}
		mediaType, _, err := resolveSendMediaType(detectSendFileMIME(f, "", data), sendMediaTypeAuto)
		if err != nil {
			return 0, 0, err
		}
		switch mediaType {
		case "image":
			images++
		case "video":
			videos++
		default:
			return 0, 0, fmt.Errorf("album files must be images or videos; %s is %s", filepath.Base(f), mediaType)
		}
	}
	return images, videos, nil
}

// sendAlbum sends an AlbumMessage, then each file as an image/video message
// associated with it (MEDIA_ALBUM), so recipients see one grouped album and
// get one notification burst instead of separate unrelated messages. The
// caption goes on the first item, as WhatsApp's own clients do.
func sendAlbum(ctx context.Context, a albumSendApp, reconnect func(context.Context) error, to types.JID, files []string, caption string) (sendAlbumOutcome, error) {
	if to.Server == types.NewsletterServer || to == types.StatusBroadcastJID {
		return sendAlbumOutcome{}, fmt.Errorf("albums are not supported for channels or status")
	}
	images, videos, err := albumMediaCounts(files)
	if err != nil {
		return sendAlbumOutcome{}, err
	}

	parentID, err := runSendOperation(ctx, reconnect, func(ctx context.Context) (types.MessageID, error) {
		return a.WA().SendProtoMessage(ctx, to, &waE2E.Message{AlbumMessage: &waE2E.AlbumMessage{
			ExpectedImageCount: proto.Uint32(images),
			ExpectedVideoCount: proto.Uint32(videos),
		}})
	})
	if err != nil {
		return sendAlbumOutcome{}, fmt.Errorf("album header: %w", err)
	}
	parent := &waCommon.MessageKey{
		RemoteJID: proto.String(to.String()),
		FromMe:    proto.Bool(true),
		ID:        proto.String(parentID),
	}

	res := sendAlbumOutcome{id: parentID}
	for i, f := range files {
		itemCaption := ""
		if i == 0 {
			itemCaption = caption
		}
		outcome, err := runFileSendOperation(ctx, reconnect, f, func(ctx context.Context) (sendFileOutcome, error) {
			return sendFile(ctx, a, to, f, sendFileOptions{caption: itemCaption, mediaAs: sendMediaTypeAuto, albumParent: parent})
		})
		if err != nil {
			return res, fmt.Errorf("album %s: sent %d of %d items, then: %w", parentID, i, len(files), err)
		}
		res.ids = append(res.ids, outcome.id)
		res.files = append(res.files, outcome.meta)
		if outcome.storeWarning != nil {
			res.storeWarning = errors.Join(res.storeWarning, outcome.storeWarning)
		}
	}
	return res, nil
}

func absAlbumFiles(files []string) []string {
	abs := make([]string, len(files))
	for i, f := range files {
		abs[i] = f
		if p, err := filepath.Abs(f); err == nil {
			abs[i] = p
		}
	}
	return abs
}

func newSendAlbumCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var files []string
	var caption string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "album",
		Short: "Send several images/videos as one WhatsApp album",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" {
				return fmt.Errorf("--to is required")
			}
			if _, _, err := albumMediaCounts(files); err != nil {
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
					Kind:           "album",
					To:             to,
					Pick:           pick,
					Files:          absAlbumFiles(files),
					Caption:        caption,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "album", resp)
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
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			toJID = warmupRecipient(ctx, a.WA(), toJID, os.Stderr)
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}
			res, err := sendAlbum(ctx, a, reconnectForSend(a), toJID, files, caption)
			if err != nil {
				return err
			}
			warnSendStoreFailure(os.Stderr, res.id, res.storeWarning)
			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, addStoreWarning(map[string]any{
					"sent":  true,
					"to":    toJID.String(),
					"id":    res.id,
					"ids":   res.ids,
					"files": res.files,
				}, res.storeWarning))
			}
			fmt.Fprintf(os.Stdout, "Sent album of %d to %s (id %s)\n", len(res.ids), toJID.String(), res.id)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringArrayVar(&files, "file", nil, "image or video path (repeat 2-30 times, in album order)")
	cmd.Flags().StringVar(&caption, "caption", "", "caption, shown on the first item")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

func executeDelegatedAlbum(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	res, err := sendAlbum(ctx, a, reconnectForSend(a), toJID, req.Files, req.Caption)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	resp := sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: res.id, IDs: res.ids, Files: res.files}
	if res.storeWarning != nil {
		resp.StoreWarning = res.storeWarning.Error()
	}
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	return resp, nil
}
