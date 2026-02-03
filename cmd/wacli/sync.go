package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
)

func newSyncCmd(flags *rootFlags) *cobra.Command {
	var once bool
	var follow bool
	var idleExit time.Duration
	var downloadMedia bool
	var mediaDir string
	var refreshContacts bool
	var refreshGroups bool
	var markRead bool
	var output string

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

			// If --media is set, enable media download with custom dir.
			if mediaDir != "" {
				downloadMedia = true
			}

			// Set up output mode.
			outputMode := appPkg.OutputNone
			switch strings.ToLower(strings.TrimSpace(output)) {
			case "text":
				outputMode = appPkg.OutputText
			case "json":
				outputMode = appPkg.OutputJSON
			case "none", "":
				outputMode = appPkg.OutputNone
			default:
				return fmt.Errorf("unknown output mode %q (use none, text, or json)", output)
			}

			// Build OnMessage callback based on output mode.
			var onMessage func(pm wa.ParsedMessage)
			switch outputMode {
			case appPkg.OutputText:
				onMessage = func(pm wa.ParsedMessage) {
					text := strings.ReplaceAll(pm.Text, "\n", " ")
					if len(text) > 100 {
						text = text[:100] + "…"
					}
					sender := pm.SenderJID
					if pm.FromMe {
						sender = "me"
					}
					fmt.Fprintf(os.Stdout, "from=%s chat=%s id=%s text=%s\n",
						sender, pm.Chat.String(), pm.ID, text)
				}
			case appPkg.OutputJSON:
				enc := json.NewEncoder(os.Stdout)
				onMessage = func(pm wa.ParsedMessage) {
					obj := map[string]interface{}{
						"from_me":    pm.FromMe,
						"sender":     pm.SenderJID,
						"chat":       pm.Chat.String(),
						"id":         pm.ID,
						"timestamp":  pm.Timestamp.UTC().Format(time.RFC3339),
						"text":       pm.Text,
						"push_name":  pm.PushName,
						"has_media":  pm.Media != nil,
						"reply_to":   pm.ReplyToID,
						"reaction":   pm.ReactionEmoji,
						"reaction_to": pm.ReactionToID,
					}
					if pm.Media != nil {
						obj["media_type"] = pm.Media.Type
						obj["mime_type"] = pm.Media.MimeType
						obj["filename"] = pm.Media.Filename
						obj["caption"] = pm.Media.Caption
					}
					_ = enc.Encode(obj)
				}
			}

			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:            mode,
				AllowQR:         false,
				DownloadMedia:   downloadMedia,
				MediaDir:        mediaDir,
				RefreshContacts: refreshContacts,
				RefreshGroups:   refreshGroups,
				IdleExit:        idleExit,
				OnMessage:       onMessage,
				MarkRead:        markRead,
				Output:          outputMode,
				EnableSocket:    true, // always enable socket server during sync
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
	cmd.Flags().StringVar(&mediaDir, "media", "", "download media to this directory (implies --download-media)")
	cmd.Flags().BoolVar(&refreshContacts, "refresh-contacts", false, "refresh contacts from session store into local DB")
	cmd.Flags().BoolVar(&refreshGroups, "refresh-groups", false, "refresh joined groups (live) into local DB")
	cmd.Flags().BoolVar(&markRead, "mark-read", false, "automatically mark incoming messages as read")
	cmd.Flags().StringVar(&output, "output", "none", "message output mode: none, text, or json")
	return cmd
}
