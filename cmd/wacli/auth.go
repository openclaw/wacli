package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func newAuthCmd(flags *rootFlags) *cobra.Command {
	var follow bool
	var idleExit time.Duration
	var downloadMedia bool
	var qrFormat string
	var phone string

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with WhatsApp (QR) and bootstrap sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			qrFormat, err := normalizeAuthQRFormat(qrFormat)
			if err != nil {
				return err
			}
			if flags.asJSON && qrFormat == "text" {
				return fmt.Errorf("--qr-format=text cannot be combined with --json because both write to stdout")
			}
			pairPhone, err := normalizePairPhone(phone)
			if err != nil {
				return err
			}
			maxMessages, maxDBSize, err := resolveSyncStorageLimits(syncStorageLimitFlags{})
			if err != nil {
				return err
			}
			ctx, stop := signalContextWithEvents(out.NewEventWriter(os.Stderr, flags.events))
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

			if a.Events().Enabled() {
				_ = a.Events().Emit("auth_starting", nil)
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
				OnQRCode:        authQRWriter(qrFormat, os.Stdout, os.Stderr, a.Events()),
				PairPhoneNumber: pairPhone,
				OnPairCode:      authPairCodeWriter(pairPhone, os.Stderr, a.Events()),
				MaxMessages:     maxMessages,
				MaxDBSizeBytes:  maxDBSize,
				WarnNoLimits:    true,
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
	cmd.Flags().StringVar(&qrFormat, "qr-format", "terminal", "QR output format: terminal or text")
	cmd.Flags().StringVar(&phone, "phone", "", "pair by phone number instead of QR code")

	cmd.AddCommand(newAuthStatusCmd(flags))
	cmd.AddCommand(newAuthLogoutCmd(flags))

	return cmd
}

func normalizePairPhone(phone string) (string, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return "", nil
	}
	jid, err := wa.ParseUserOrJID(phone)
	if err != nil {
		return "", fmt.Errorf("invalid --phone: %w", err)
	}
	if jid.Server != types.DefaultUserServer || jid.Device != 0 {
		return "", fmt.Errorf("invalid --phone: must be an international phone number")
	}
	return jid.User, nil
}

func normalizeAuthQRFormat(format string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "terminal"
	}
	switch format {
	case "terminal", "text":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported --qr-format %q (want terminal or text)", format)
	}
}

func authQRWriter(format string, stdout, stderr io.Writer, events *out.EventWriter) func(string) {
	if format == "text" {
		return func(code string) {
			if events.Enabled() {
				_ = events.Emit("qr_code", map[string]any{"code": code})
			}
			fmt.Fprintln(stdout, code)
		}
	}
	return func(code string) {
		if events.Enabled() {
			_ = events.Emit("qr_code", map[string]any{"code": code})
			return
		}
		fmt.Fprintln(stderr, "\nScan this QR code with WhatsApp (Linked Devices):")
		qrterminal.GenerateHalfBlock(code, qrterminal.M, stderr)
		fmt.Fprintln(stderr)
	}
}

func authPairCodeWriter(phone string, stderr io.Writer, events *out.EventWriter) func(string) {
	if phone == "" {
		return nil
	}
	return func(code string) {
		if events.Enabled() {
			_ = events.Emit("pair_code", map[string]any{"phone": phone, "code": code})
			return
		}
		fmt.Fprintf(stderr, "\nPairing code for +%s: %s\n", phone, code)
		fmt.Fprintln(stderr, "On your phone: WhatsApp > Linked Devices > Link a Device > Link with phone number.")
		fmt.Fprintln(stderr, "Enter the code above and keep this command running until authentication completes.")
	}
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
			var linkedJID string
			if authed {
				linkedJID = a.WA().LinkedJID()
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, authStatusPayload(authed, linkedJID))
			}
			writeAuthStatus(os.Stdout, authed, linkedJID)
			return nil
		},
	}
}

func authStatusPayload(authed bool, linkedJID string) map[string]any {
	data := map[string]any{"authenticated": authed}
	if !authed || linkedJID == "" {
		return data
	}
	data["linked_jid"] = linkedJID
	if phone := phoneFromLinkedJID(linkedJID); phone != "" {
		data["phone"] = phone
	}
	return data
}

func writeAuthStatus(w io.Writer, authed bool, linkedJID string) {
	if !authed {
		fmt.Fprintln(w, "Not authenticated. Run `wacli auth`.")
		return
	}
	if linkedJID != "" {
		fmt.Fprintf(w, "Authenticated as %s\n", linkedJID)
		return
	}
	fmt.Fprintln(w, "Authenticated.")
}

func phoneFromLinkedJID(linkedJID string) string {
	phone, _, ok := strings.Cut(linkedJID, "@")
	if !ok {
		return ""
	}
	return phone
}

func newAuthLogoutCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Logout (invalidate session)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
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
