package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	appPkg "github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
)

func newSendFileCmd(flags *rootFlags) *cobra.Command {
	var to string
	var filePath string
	var filename string
	var caption string
	var mimeOverride string

	cmd := &cobra.Command{
		Use:   "file",
		Short: "Send a file (image/video/audio/document)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" || filePath == "" {
				return fmt.Errorf("--to and --file are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			storeDir := resolveStoreDir(flags)

			// Try socket first (sync process is running).
			if appPkg.IsSocketAvailable(storeDir) {
				b64, err := appPkg.EncodeFileToBase64(filePath)
				if err != nil {
					return fmt.Errorf("read file: %w", err)
				}
				fn := filename
				if fn == "" {
					fn = filepath.Base(filePath)
				}
				resp, err := appPkg.SendSocketRequest(storeDir, appPkg.SocketRequest{
					Action:       "send_file",
					To:           to,
					FileDataB64:  b64,
					Filename:     fn,
					Caption:      caption,
					MimeOverride: mimeOverride,
				})
				if err != nil {
					return fmt.Errorf("socket send failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("send failed: %s", resp.Error)
				}
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"sent": true,
						"to":   to,
						"id":   resp.ID,
						"file": resp.Meta,
					})
				}
				name := fn
				if resp.Meta != nil && resp.Meta["name"] != "" {
					name = resp.Meta["name"]
				}
				fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", name, to, resp.ID)
				return nil
			}

			// Fallback: direct connection.
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

			toJID, err := wa.ParseUserOrJID(to)
			if err != nil {
				return err
			}

			msgID, meta, err := sendFile(ctx, a, toJID, filePath, filename, caption, mimeOverride)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent": true,
					"to":   toJID.String(),
					"id":   msgID,
					"file": meta,
				})
			}
			fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", meta["name"], toJID.String(), msgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number or JID")
	cmd.Flags().StringVar(&filePath, "file", "", "path to file")
	cmd.Flags().StringVar(&filename, "filename", "", "display name for the file (defaults to basename of --file)")
	cmd.Flags().StringVar(&caption, "caption", "", "caption (images/videos/documents)")
	cmd.Flags().StringVar(&mimeOverride, "mime", "", "override detected mime type")
	return cmd
}
