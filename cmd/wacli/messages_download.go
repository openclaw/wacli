package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newMessagesDownloadCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var output string

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download a media message (image / video / audio / document) to disk",
		Long: `Download the media payload of a message to a file.

Resolves the message in the local store, fetches the encrypted blob from
WhatsApp's CDN using the stored direct_path / media_key / hashes, and
decrypts it to the target path. Requires an authenticated session.

Examples:
  wacli messages download --chat 1203630000000@g.us --id 3EB0AAA --output ./out.jpg
  wacli messages download --chat <jid> --id <msgid>            # auto path: ./<msgid>.<ext>
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" || id == "" {
				return fmt.Errorf("--chat and --id are required")
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			info, err := a.DB().GetMediaDownloadInfo(chat, id)
			if err != nil {
				return fmt.Errorf("lookup message: %w", err)
			}
			if info.MsgID == "" {
				return fmt.Errorf("message not found: chat=%s id=%s", chat, id)
			}
			if info.MediaType == "" {
				return fmt.Errorf("message %s is not a media message", id)
			}
			if info.DirectPath == "" || len(info.MediaKey) == 0 || len(info.FileEncSHA256) == 0 {
				return fmt.Errorf("message %s lacks media metadata required for download (direct_path/media_key/file_enc_sha256 missing)", id)
			}

			target := output
			if target == "" {
				target = defaultDownloadPath(info.MsgID, info.Filename, info.MimeType, info.MediaType)
			}
			if abs, err := filepath.Abs(target); err == nil {
				target = abs
			}

			if err := a.Connect(ctx, false, nil); err != nil {
				return fmt.Errorf("connect: %w", err)
			}

			// mmsType for whatsmeow's CDN protocol is usually the same string
			// as the high-level media type ("image" / "video" / "audio" /
			// "document" / "sticker"). Pass it through verbatim.
			size, err := a.WA().DownloadMediaToFile(
				ctx,
				info.DirectPath,
				info.FileEncSHA256,
				info.FileSHA256,
				info.MediaKey,
				info.FileLength,
				info.MediaType,
				info.MediaType,
				target,
			)
			if err != nil {
				return fmt.Errorf("download: %w", err)
			}

			result := map[string]any{
				"msg_id":     info.MsgID,
				"chat_jid":   info.ChatJID,
				"path":       target,
				"size_bytes": size,
				"media_type": info.MediaType,
				"mime_type":  info.MimeType,
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, result)
			}
			fmt.Fprintf(os.Stdout, "downloaded %d bytes → %s\n", size, target)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().StringVar(&id, "id", "", "message ID")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output file path (default: ./<msgid>.<ext>)")
	return cmd
}

// defaultDownloadPath builds a sensible local filename when --output isn't given.
// Preference order: stored filename → msg_id + extension from mime/media type.
func defaultDownloadPath(msgID, filename, mimeType, mediaType string) string {
	if filename != "" {
		return "./" + sanitizeFilename(filename)
	}
	ext := extensionFor(mimeType, mediaType)
	return "./" + msgID + ext
}

// sanitizeFilename strips path separators and other characters that would
// escape the working dir or confuse the shell. Conservative: only keep
// alnum, dot, dash, underscore.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "download"
	}
	return out
}

func extensionFor(mimeType, mediaType string) string {
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	switch {
	case strings.HasPrefix(mimeType, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mimeType, "image/png"):
		return ".png"
	case strings.HasPrefix(mimeType, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mimeType, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mimeType, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mimeType, "video/3gpp"):
		return ".3gp"
	case strings.HasPrefix(mimeType, "audio/ogg"):
		return ".ogg"
	case strings.HasPrefix(mimeType, "audio/mpeg"), strings.HasPrefix(mimeType, "audio/mp3"):
		return ".mp3"
	case strings.HasPrefix(mimeType, "application/pdf"):
		return ".pdf"
	}
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image", "sticker":
		return ".jpg"
	case "video":
		return ".mp4"
	case "audio":
		return ".ogg"
	case "document":
		return ".bin"
	}
	return ""
}

// errors below kept exported-ish for tests if added later.
var _ = errors.New
