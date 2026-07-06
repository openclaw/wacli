package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
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
	cmd.AddCommand(newMediaBackfillCmd(flags))
	cmd.AddCommand(newMediaRetryCmd(flags))
	return cmd
}

func newMediaRetryCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var limit int
	var batch int
	var wait time.Duration
	var before string

	cmd := &cobra.Command{
		Use:   "retry",
		Short: "Recover expired media by asking the phone to re-upload it",
		Long: "For media that expired off WhatsApp's CDN, ask the primary device (phone)\n" +
			"to re-upload it via the media-retry protocol, then download it. Receipts are\n" +
			"sent in batches with a second attempt for non-responders; media the phone no\n" +
			"longer holds is marked so it is not retried again. Only works while the phone\n" +
			"is online and still has the media.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be >= 0")
			}
			if batch <= 0 {
				return fmt.Errorf("--batch must be > 0")
			}
			if wait <= 0 {
				return fmt.Errorf("--wait must be > 0")
			}
			beforeUnix, err := parseMediaRetryBefore(before)
			if err != nil {
				return err
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := mediaBulkContext(cmd, flags)
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

			res, err := a.RetryMedia(ctx, app.RetryMediaOptions{
				ChatJID:    strings.TrimSpace(chat),
				BeforeUnix: beforeUnix,
				BeforeSet:  strings.TrimSpace(before) != "",
				Limit:      limit,
				BatchSize:  batch,
				Wait:       wait,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}
			fmt.Fprintf(os.Stdout, "Requested: %d  Recovered: %d  Not on phone: %d  No response: %d  Failed: %d\n",
				res.Requested, res.Recovered, res.NotOnPhone, res.NoResponse, res.Failed)
			for _, o := range res.Outcomes {
				line := fmt.Sprintf("  %-13s %s/%s", o.Status, o.ChatJID, o.MsgID)
				if o.Status == "recovered" {
					line += fmt.Sprintf("  (%d bytes) %s", o.Bytes, o.Path)
				} else if o.Detail != "" {
					line += "  " + o.Detail
				}
				fmt.Fprintln(os.Stdout, line)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "limit retry to a single chat JID")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of messages to retry (0 = all pending)")
	cmd.Flags().IntVar(&batch, "batch", 32, "number of retry receipts to send per batch")
	cmd.Flags().DurationVar(&wait, "wait", 30*time.Second, "how long to wait for the phone per attempt")
	cmd.Flags().StringVar(&before, "before", "", "only retry media older than this date (YYYY-MM-DD)")
	return cmd
}

func parseMediaRetryBefore(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return 0, fmt.Errorf("--before must be YYYY-MM-DD: %w", err)
	}
	return parsed.Unix(), nil
}

func newMediaBackfillCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var limit int
	var workers int

	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Download media for already-synced messages missing a local copy",
		Long: "Fetch media for messages already stored in the local database that have\n" +
			"downloadable metadata but no local file yet. Unlike `sync --download-media`,\n" +
			"which only downloads media for messages arriving during the sync, this scans\n" +
			"existing rows and downloads them over a single connection.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be >= 0")
			}
			if workers < 0 {
				return fmt.Errorf("--workers must be >= 0")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := mediaBulkContext(cmd, flags)
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

			res, err := a.BackfillMedia(ctx, app.BackfillMediaOptions{
				ChatJID: chat,
				Limit:   limit,
				Workers: workers,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"pending":    res.Pending,
					"attempted":  res.Attempted,
					"downloaded": res.Downloaded,
					"skipped":    res.Skipped,
					"failed":     res.Failed,
				})
			}
			fmt.Fprintf(os.Stdout, "Pending: %d  Attempted: %d  Downloaded: %d  Skipped: %d  Failed: %d\n",
				res.Pending, res.Attempted, res.Downloaded, res.Skipped, res.Failed)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "limit backfill to a single chat JID")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of media files to download (0 = all)")
	cmd.Flags().IntVar(&workers, "workers", 4, "number of concurrent downloads")
	return cmd
}

func mediaBulkContext(cmd *cobra.Command, flags *rootFlags) (context.Context, context.CancelFunc) {
	ctx, stop := signalContextWithEvents(out.NewEventWriter(os.Stderr, flags.events))
	if !mediaBulkTimeoutEnabled(cmd, flags) {
		return ctx, stop
	}
	timedCtx, cancel := withTimeout(ctx, flags)
	return timedCtx, func() {
		cancel()
		stop()
	}
}

func mediaBulkTimeoutEnabled(cmd *cobra.Command, flags *rootFlags) bool {
	if cmd == nil || flags == nil || flags.timeout <= 0 {
		return false
	}
	if flag := cmd.Flags().Lookup("timeout"); flag != nil && flag.Changed {
		return true
	}
	flag := cmd.InheritedFlags().Lookup("timeout")
	return flag != nil && flag.Changed
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
