package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
)

func newAuthCmd(flags *rootFlags) *cobra.Command {
	var follow bool
	var idleExit time.Duration
	var downloadMedia bool

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with WhatsApp (QR) and bootstrap sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			mode := appPkg.SyncModeBootstrap
			if follow {
				mode = appPkg.SyncModeFollow
			}

			ev := a.Events()
			if ev.Enabled() {
				ev.Emit("auth_starting", nil)
			} else {
				fmt.Fprintln(os.Stderr, "Starting authentication…")
			}
			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:            mode,
				AllowQR:         true,
				DownloadMedia:   downloadMedia,
				RefreshContacts: true,
				RefreshGroups:   true,
				IdleExit:        idleExit,
				OnQRCode: func(code string) {
					if ev.Enabled() {
						ev.Emit("qr_code", map[string]interface{}{"code": code})
					} else {
						fmt.Fprintln(os.Stderr, "\nScan this QR code with WhatsApp (Linked Devices):")
						qrterminal.GenerateHalfBlock(code, qrterminal.M, os.Stderr)
						fmt.Fprintln(os.Stderr)
					}
				},
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]interface{}{
					"authenticated":   true,
					"messages_stored": res.MessagesStored,
				})
			}

			fmt.Fprintf(os.Stdout, "Authenticated. Messages stored: %d\n", res.MessagesStored)
			return nil
		},
	}

	cmd.Flags().BoolVar(&follow, "follow", false, "keep syncing after auth")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 30*time.Second, "exit after being idle (bootstrap/once modes)")
	cmd.Flags().BoolVar(&downloadMedia, "download-media", false, "download media in the background during sync")

	cmd.AddCommand(newAuthStatusCmd(flags))
	cmd.AddCommand(newAuthLogoutCmd(flags))

	return cmd
}

func newAuthStatusCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show authentication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.OpenWA(); err != nil {
				return err
			}
			authed := a.WA().IsAuthed()

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"authenticated": authed,
				})
			}
			if authed {
				fmt.Fprintln(os.Stdout, "Authenticated.")
			} else {
				fmt.Fprintln(os.Stdout, "Not authenticated. Run `wacli auth`.")
			}
			return nil
		},
	}
}

func newAuthLogoutCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Logout (invalidate session)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, true)
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
			if err := a.WA().Logout(ctx); err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"logged_out": true})
			}
			fmt.Fprintln(os.Stdout, "Logged out.")
			return nil
		},
	}
}
