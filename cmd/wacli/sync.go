package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/ipc"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
)

// syncHandler implements ipc.Handler for the sync daemon.
type syncHandler struct {
	app *appPkg.App
}

func (h *syncHandler) SendText(to, message string) (string, error) {
	if h.app == nil {
		return "", fmt.Errorf("app not initialized")
	}
	if h.app.WA() == nil {
		return "", fmt.Errorf("whatsapp client not initialized")
	}
	if !h.app.WA().IsConnected() {
		return "", fmt.Errorf("whatsapp not connected")
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	toJID, err := wa.ParseUserOrJID(to)
	if err != nil {
		return "", fmt.Errorf("parse recipient: %w", err)
	}
	
	msgID, err := h.app.WA().SendText(ctx, toJID, message)
	if err != nil {
		return "", fmt.Errorf("send: %w", err)
	}
	
	// Store the message in the local DB
	now := time.Now().UTC()
	chat := toJID
	chatName := h.app.WA().ResolveChatName(ctx, chat, "")
	kind := chatKindFromJID(chat)
	_ = h.app.DB().UpsertChat(chat.String(), kind, chatName, now)
	_ = h.app.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    chat.String(),
		ChatName:   chatName,
		MsgID:      string(msgID),
		SenderJID:  "",
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       message,
	})
	
	return string(msgID), nil
}

func (h *syncHandler) DeleteMessage(chat, msgID string, forEveryone bool) error {
	if h.app == nil {
		return fmt.Errorf("app not initialized")
	}
	if h.app.WA() == nil {
		return fmt.Errorf("whatsapp client not initialized")
	}
	if !h.app.WA().IsConnected() {
		return fmt.Errorf("whatsapp not connected")
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	chatJID, err := wa.ParseUserOrJID(chat)
	if err != nil {
		return fmt.Errorf("parse chat: %w", err)
	}
	
	// Type assert to get the concrete client
	waClient, ok := h.app.WA().(*wa.Client)
	if !ok {
		return fmt.Errorf("unexpected WA client type")
	}
	
	return waClient.RevokeMessage(ctx, chatJID, msgID, forEveryone)
}

func newSyncCmd(flags *rootFlags) *cobra.Command {
	var once bool
	var follow bool
	var idleExit time.Duration
	var downloadMedia bool
	var refreshContacts bool
	var refreshGroups bool
	var enableIPC bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync messages (requires prior auth; never shows QR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}

			mode := appPkg.SyncModeFollow
			if once {
				mode = appPkg.SyncModeOnce
			} else if follow {
				mode = appPkg.SyncModeFollow
			} else {
				mode = appPkg.SyncModeOnce
			}

			// Start IPC server if enabled (default for --follow mode)
			var ipcServer *ipc.Server
			if enableIPC && mode == appPkg.SyncModeFollow {
				handler := &syncHandler{app: a}
				ipcServer = ipc.NewServer(a.StoreDir(), handler)
				if err := ipcServer.Start(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to start IPC server: %v\n", err)
				} else {
					defer ipcServer.Stop()
				}
			}

			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:            mode,
				AllowQR:         false,
				DownloadMedia:   downloadMedia,
				RefreshContacts: refreshContacts,
				RefreshGroups:   refreshGroups,
				IdleExit:        idleExit,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"synced":          true,
					"messages_stored": res.MessagesStored,
				})
			}
			fmt.Fprintf(os.Stdout, "Messages stored: %d\n", res.MessagesStored)
			return nil
		},
	}

	cmd.Flags().BoolVar(&once, "once", false, "sync until idle and exit")
	cmd.Flags().BoolVar(&follow, "follow", true, "keep syncing until Ctrl+C")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 30*time.Second, "exit after being idle (once mode)")
	cmd.Flags().BoolVar(&downloadMedia, "download-media", false, "download media in the background during sync")
	cmd.Flags().BoolVar(&refreshContacts, "refresh-contacts", false, "refresh contacts from session store into local DB")
	cmd.Flags().BoolVar(&refreshGroups, "refresh-groups", false, "refresh joined groups (live) into local DB")
	cmd.Flags().BoolVar(&enableIPC, "enable-ipc", true, "enable IPC socket for send commands (--follow mode only)")
	return cmd
}
