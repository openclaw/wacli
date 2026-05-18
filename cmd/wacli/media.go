package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
)

func newMediaCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Media download",
	}
	cmd.AddCommand(newMediaDownloadCmd(flags))
	return cmd
}

func newMediaDownloadCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var outputPath string

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download media for a message",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || id == "" {
				return fmt.Errorf("--chat and --id are required")
			}
			readOnly := flags.isReadOnly()
			if readOnly {
				if strings.TrimSpace(outputPath) == "" {
					return fmt.Errorf("--output is required in read-only mode")
				}
			} else {
				if err := flags.requireWritable(); err != nil {
					return err
				}
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, !readOnly, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if !readOnly {
				if err := a.EnsureAuthed(); err != nil {
					return err
				}
			}

			info, err := a.DB().GetMediaDownloadInfo(chat, id)
			if err != nil {
				return err
			}
			if info.MediaType == "" || info.DirectPath == "" || len(info.MediaKey) == 0 {
				return fmt.Errorf("message has no downloadable media metadata (run `wacli sync` first)")
			}

			target, err := a.ResolveMediaOutputPath(info, outputPath)
			if err != nil {
				return err
			}

			if readOnly {
				bytes, err := wa.DownloadMediaDirectToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, target)
				if err != nil {
					return err
				}
				resp := map[string]any{
					"chat":       info.ChatJID,
					"id":         info.MsgID,
					"path":       target,
					"bytes":      bytes,
					"media_type": info.MediaType,
					"mime_type":  info.MimeType,
					"downloaded": true,
					"read_only":  true,
					"recorded":   false,
				}
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, resp)
				}
				fmt.Fprintf(os.Stdout, "%s (%d bytes)\n", target, bytes)
				return nil
			}

			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			bytes, err := a.WA().DownloadMediaToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, info.MediaType, "", target)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			if err := a.DB().MarkMediaDownloaded(info.ChatJID, info.MsgID, target, now); err != nil {
				return fmt.Errorf("record media download: %w", err)
			}

			resp := map[string]any{
				"chat":          info.ChatJID,
				"id":            info.MsgID,
				"path":          target,
				"bytes":         bytes,
				"media_type":    info.MediaType,
				"mime_type":     info.MimeType,
				"downloaded":    true,
				"downloaded_at": now.Format(time.RFC3339Nano),
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, resp)
			}
			fmt.Fprintf(os.Stdout, "%s (%d bytes)\n", target, bytes)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&id, "id", "", "message ID")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file or directory (default: store media dir)")
	_ = cmd.MarkFlagRequired("chat")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
