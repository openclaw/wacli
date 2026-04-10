package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
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

			type sendFileResult struct {
				id   string
				meta map[string]string
			}
			res, err := runSendOperation(ctx, func(ctx context.Context) error {
				a.WA().Close()
				return a.Connect(ctx, false, nil)
			}, func(ctx context.Context) (sendFileResult, error) {
				msgID, meta, err := sendFile(ctx, a, toJID, filePath, filename, caption, mimeOverride)
				if err != nil {
					return sendFileResult{}, err
				}
				return sendFileResult{id: msgID, meta: meta}, nil
			})
			if err != nil {
				return err
			}
			msgID, meta := res.id, res.meta

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
