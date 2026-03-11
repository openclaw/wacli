package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/lock"
	"github.com/steipete/wacli/internal/out"
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

			absFilePath, err := filepath.Abs(filePath)
			if err != nil {
				return fmt.Errorf("resolve file path: %w", err)
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			params := app.SendFileParams{
				To:       to,
				FilePath: absFilePath,
				Filename: filename,
				Caption:  caption,
				MIMEType: mimeOverride,
			}

			var res app.SendResult
			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				if !errors.Is(err, lock.ErrLocked) {
					return err
				}
				lockErr := err
				res, err = app.DelegateSendFile(ctx, resolveStoreDir(flags), params)
				if err != nil {
					if errors.Is(err, app.ErrSendDelegateUnavailable) {
						return lockErr
					}
					return err
				}
			} else {
				defer closeApp(a, lk)
				res, err = a.SendFile(ctx, params)
				if err != nil {
					return err
				}
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"sent": true,
					"to":   res.To,
					"id":   res.ID,
					"file": map[string]string{
						"name":      res.File.Name,
						"mime_type": res.File.MIMEType,
						"media":     res.File.Media,
					},
				})
			}
			fmt.Fprintf(os.Stdout, "Sent %s to %s (id %s)\n", res.File.Name, res.To, res.ID)
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
