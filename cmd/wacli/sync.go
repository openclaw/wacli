package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
)

func newSyncCmd(flags *rootFlags) *cobra.Command {
	var once bool
	var follow bool
	var idleExit time.Duration
	var maxReconnect time.Duration
	var downloadMedia bool
	var refreshContacts bool
	var refreshGroups bool
	var storage syncStorageLimitFlags

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync messages (requires prior auth; never shows QR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			maxMessages, maxDBSize, err := resolveSyncStorageLimits(storage)
			if err != nil {
				return err
			}
			ctx, stop := signalContextWithEvents(out.NewEventWriter(os.Stderr, flags.events))
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

			var stopSendDelegate func()
			defer func() {
				if stopSendDelegate != nil {
					stopSendDelegate()
				}
			}()
			var afterConnect func(context.Context) error
			if mode == appPkg.SyncModeFollow {
				afterConnect = func(ctx context.Context) error {
					stop, err := startSendDelegateServer(ctx, a)
					if err != nil {
						return err
					}
					stopSendDelegate = stop
					return nil
				}
			}

			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:            mode,
				AllowQR:         false,
				AfterConnect:    afterConnect,
				DownloadMedia:   downloadMedia,
				RefreshContacts: refreshContacts,
				RefreshGroups:   refreshGroups,
				IdleExit:        idleExit,
				MaxReconnect:    maxReconnect,
				MaxMessages:     maxMessages,
				MaxDBSizeBytes:  maxDBSize,
				WarnNoLimits:    true,
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
	cmd.Flags().DurationVar(&maxReconnect, "max-reconnect", 5*time.Minute, "give up reconnecting after this duration (0 = unlimited)")
	cmd.Flags().BoolVar(&downloadMedia, "download-media", false, "download media in the background during sync")
	cmd.Flags().BoolVar(&refreshContacts, "refresh-contacts", false, "refresh contacts from session store into local DB")
	cmd.Flags().BoolVar(&refreshGroups, "refresh-groups", false, "refresh joined groups (live) into local DB")
	cmd.Flags().Int64Var(&storage.maxMessages, "max-messages", 0, "maximum total messages to keep in the local DB before sync stops (0 = unlimited, or WACLI_SYNC_MAX_MESSAGES)")
	cmd.Flags().StringVar(&storage.maxDBSize, "max-db-size", "", "maximum wacli.db disk usage before sync stops, e.g. 500MB or 2GB (default: WACLI_SYNC_MAX_DB_SIZE or unlimited)")
	return cmd
}
