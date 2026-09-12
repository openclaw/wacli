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
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newMessagesDeleteCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var forMe bool
	var deleteMedia bool
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a message for everyone or for you",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(chat) == "" || strings.TrimSpace(id) == "" {
				return fmt.Errorf("--chat and --id are required")
			}
			if deleteMedia && !forMe {
				return fmt.Errorf("--delete-media requires --for-me")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			msg, chatJID, err := loadMessageMutationTarget(ctx, a, chat, id)
			if err != nil {
				return err
			}
			if !forMe {
				if err := validateMessageCanRevoke(msg); err != nil {
					return err
				}
			} else if err := validateMessageCanDeleteForMe(msg); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}
			if forMe {
				info, err := messageInfoForDeleteForMe(msg, chatJID)
				if err != nil {
					return err
				}
				if _, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (struct{}, error) {
					return struct{}{}, a.WA().DeleteMessageForMe(ctx, info, deleteMedia)
				}); err != nil {
					return err
				}
				mediaPaths, err := a.DB().MessageLocalMediaPaths(msg.ChatJID, msg.MsgID)
				if err != nil {
					return fmt.Errorf("load local media paths: %w", err)
				}
				deletedMediaCount, deleteMediaErr := deleteLocalMediaPathsIfRequested(deleteMedia, mediaPaths)
				deletedLocalMedia := deletedMediaCount > 0
				if deleteMediaErr != nil {
					if err := a.DB().MarkMessageDeletedForMePreserveMedia(msg.ChatJID, msg.MsgID); err != nil {
						return fmt.Errorf("store deleted-for-me message state: %w", err)
					}
					return fmt.Errorf("delete local media: %w", deleteMediaErr)
				}
				if deleteMedia && strings.TrimSpace(msg.LocalPath) != "" {
					if err := a.DB().ClearMessageLocalMedia(msg.ChatJID, msg.MsgID); err != nil {
						return fmt.Errorf("clear deleted local media state: %w", err)
					}
				}
				if err := a.DB().MarkMessageDeletedForMe(msg.ChatJID, msg.MsgID, msg.SenderJID, msg.FromMe, time.Now().UTC()); err != nil {
					return fmt.Errorf("store deleted-for-me message state: %w", err)
				}

				waitForPostSendRetryReceipts(ctx, postSendWait)

				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"deleted_for_me": true,
						"to":             chatJID.String(),
						"target":         msg.MsgID,
						"deleted_media":  deletedLocalMedia,
					})
				}
				fmt.Fprintf(os.Stdout, "Deleted message %s for me in %s\n", msg.MsgID, chatJID.String())
				return nil
			}
			sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return a.WA().RevokeMessage(ctx, chatJID, types.MessageID(msg.MsgID))
			})
			if err != nil {
				return err
			}
			if err := a.DB().MarkMessageRevoked(msg.ChatJID, msg.MsgID); err != nil {
				return fmt.Errorf("store deleted message state: %w", err)
			}

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"revoked": true,
					"to":      chatJID.String(),
					"id":      sentID,
					"target":  msg.MsgID,
				})
			}
			fmt.Fprintf(os.Stdout, "Deleted message %s in %s (id %s)\n", msg.MsgID, chatJID.String(), sentID)
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID, phone number, or contact/group/chat name")
	cmd.Flags().StringVar(&id, "id", "", "message ID to delete")
	cmd.Flags().BoolVar(&forMe, "for-me", false, "delete the message only for this WhatsApp account")
	cmd.Flags().BoolVar(&deleteMedia, "delete-media", false, "also remove local media when used with --for-me")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after delete so retry receipts can be handled (0 disables)")
	return cmd
}

func newMessagesRevokeCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Delete one of your sent messages for everyone",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(chat) == "" || strings.TrimSpace(id) == "" {
				return fmt.Errorf("--chat and --id are required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			msg, chatJID, found, err := loadMessageRevokeTarget(ctx, a, chat, id)
			if err != nil {
				return err
			}
			if found {
				if err := validateMessageCanRevoke(msg); err != nil {
					return err
				}
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}
			targetID := id
			if found {
				targetID = msg.MsgID
			}
			sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return a.WA().RevokeMessage(ctx, chatJID, types.MessageID(targetID))
			})
			if err != nil {
				return err
			}
			if found {
				if err := a.DB().MarkMessageRevoked(msg.ChatJID, msg.MsgID); err != nil {
					return fmt.Errorf("store deleted message state: %w", err)
				}
			}

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"revoked": true,
					"to":      chatJID.String(),
					"id":      sentID,
					"target":  id,
				})
			}
			fmt.Fprintf(os.Stdout, "Revoked message %s in %s (id %s)\n", id, chatJID.String(), sentID)
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "chat JID or phone number")
	cmd.Flags().StringVar(&id, "id", "", "message ID to revoke")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after revoke so retry receipts can be handled (0 disables)")
	return cmd
}

func loadMessageRevokeTarget(ctx context.Context, a *app.App, chat, id string) (store.Message, types.JID, bool, error) {
	msg, chatJID, err := loadMessageMutationTarget(ctx, a, chat, id)
	if err == nil {
		return msg, chatJID, true, nil
	}
	if !isNoRows(err) {
		return store.Message{}, types.JID{}, false, err
	}
	chatJID, parseErr := wa.ParseUserOrJID(chat)
	if parseErr != nil {
		return store.Message{}, types.JID{}, false, err
	}
	return store.Message{}, chatJID, false, nil
}

func deleteLocalMediaIfRequested(deleteMedia bool, localPath string) (bool, error) {
	deleted, err := deleteLocalMediaPathsIfRequested(deleteMedia, []string{localPath})
	return deleted > 0, err
}

func deleteLocalMediaPathsIfRequested(deleteMedia bool, paths []string) (int, error) {
	if !deleteMedia {
		return 0, nil
	}
	deleted := 0
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func validateMessageCanRevoke(msg store.Message) error {
	if msg.Revoked {
		return fmt.Errorf("message %s is already deleted", msg.MsgID)
	}
	if msg.DeletedForMe {
		return fmt.Errorf("message %s was deleted for me", msg.MsgID)
	}
	if !msg.FromMe {
		return fmt.Errorf("message %s was not sent by me", msg.MsgID)
	}
	return nil
}

func validateMessageCanDeleteForMe(msg store.Message) error {
	if msg.Revoked {
		return fmt.Errorf("message %s is already deleted", msg.MsgID)
	}
	if msg.DeletedForMe {
		return fmt.Errorf("message %s was deleted for me", msg.MsgID)
	}
	return nil
}

func messageInfoForDeleteForMe(msg store.Message, chat types.JID) (types.MessageInfo, error) {
	sender := types.EmptyJID
	if strings.TrimSpace(msg.SenderJID) != "" {
		parsed, err := types.ParseJID(msg.SenderJID)
		if err != nil {
			return types.MessageInfo{}, fmt.Errorf("stored sender JID is invalid: %w", err)
		}
		sender = parsed
	} else if !msg.FromMe && chat.Server == types.DefaultUserServer {
		sender = chat
	}
	if !msg.FromMe && chat.Server == types.GroupServer && sender.IsEmpty() {
		return types.MessageInfo{}, fmt.Errorf("stored sender JID is required to delete a group message for me")
	}
	return types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: msg.FromMe,
			IsGroup:  chat.Server == types.GroupServer,
		},
		ID:        types.MessageID(msg.MsgID),
		Timestamp: msg.Timestamp,
	}, nil
}
