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
	var sender string
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
				if forMe {
					return err
				}
				// Store locked by a running `sync --follow`: reuse its connection.
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:           deleteDelegateKind,
					To:             chat,
					ID:             id,
					Sender:         sender,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					warnDelegatedModeration(os.Stderr, resp)
					if flags.asJSON {
						body := map[string]any{
							"revoked":   true,
							"to":        resp.To,
							"id":        resp.ID,
							"target":    resp.Target,
							"delegated": true,
						}
						if resp.StoreWarning != "" {
							body["store_warning"] = resp.StoreWarning
						}
						return out.WriteJSON(os.Stdout, body)
					}
					fmt.Fprintf(os.Stdout, "Deleted message %s in %s (id %s, via running sync)\n", sanitize(resp.Target), sanitize(resp.To), sanitize(resp.ID))
					return nil
				}
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
			revokeSender := types.EmptyJID
			if !forMe {
				if revokeSender, err = resolveRevokeSender(msg, true, chatJID, sender); err != nil {
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
				return a.WA().RevokeMessage(ctx, chatJID, revokeSender, types.MessageID(msg.MsgID))
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
	cmd.Flags().StringVar(&sender, "sender", "", "original sender JID for a group admin delete (overrides the stored sender)")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after delete so retry receipts can be handled (0 disables)")
	return cmd
}

func newMessagesRevokeCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var sender string
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
			revokeSender, err := resolveRevokeSender(msg, found, chatJID, sender)
			if err != nil {
				return err
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
				return a.WA().RevokeMessage(ctx, chatJID, revokeSender, types.MessageID(targetID))
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
	cmd.Flags().StringVar(&sender, "sender", "", "original sender JID for a group admin revoke (needed when the message is not in the local DB)")
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

// resolveRevokeSender returns the sender JID to put in the revoke key. Own
// messages (and non-group chats) use an empty JID and keep the own-message
// check. For another member's message in a group, the override or the stored
// sender is used so whatsmeow sends an admin revoke.
func resolveRevokeSender(msg store.Message, found bool, chat types.JID, override string) (types.JID, error) {
	override = strings.TrimSpace(override)
	isGroup := chat.Server == types.GroupServer
	if override != "" && !isGroup {
		return types.EmptyJID, fmt.Errorf("--sender is only valid for group chats (%s is not a group); omit --sender to delete your own message", sanitize(chat.String()))
	}
	if found && (msg.FromMe || !isGroup) {
		return types.EmptyJID, validateMessageCanRevoke(msg)
	}
	if found {
		if err := validateMessageCanDeleteForMe(msg); err != nil {
			return types.EmptyJID, err
		}
	}
	senderStr := override
	if senderStr == "" && found {
		senderStr = strings.TrimSpace(msg.SenderJID)
	}
	if senderStr == "" {
		if !found {
			return types.EmptyJID, nil
		}
		return types.EmptyJID, fmt.Errorf("message %s has no stored sender; pass --sender with the original sender's phone number or JID", sanitize(msg.MsgID))
	}
	sender, err := wa.ParseUserOrJID(senderStr)
	if err != nil {
		return types.EmptyJID, fmt.Errorf("invalid sender %q: %v; pass a phone number, @s.whatsapp.net or @lid JID", sanitize(senderStr), err)
	}
	if sender.User == "" || (sender.Server != types.DefaultUserServer && sender.Server != types.HiddenUserServer) {
		return types.EmptyJID, fmt.Errorf("invalid sender %q: must be a user (@s.whatsapp.net or @lid), not a group or broadcast JID", sanitize(senderStr))
	}
	return sender.ToNonAD(), nil
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
