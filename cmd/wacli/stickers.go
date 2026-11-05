package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/pathutil"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
)

func newStickersCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stickers",
		Short: "List and download your favorite and recent stickers",
	}
	cmd.AddCommand(newStickersListCmd(flags))
	cmd.AddCommand(newStickersDownloadCmd(flags))
	cmd.AddCommand(newStickersRefreshCmd(flags))
	return cmd
}

func newStickersListCmd(flags *rootFlags) *cobra.Command {
	var favorites, recent bool
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List favorite and recent stickers from the local DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be >= 0")
			}
			ctx, cancel := withTimeout(cmd.Context(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			stickers, err := a.DB().ListStickers(store.ListStickersParams{Favorites: favorites, Recent: recent, Limit: limit})
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"stickers": stickers})
			}
			return writeStickersList(os.Stdout, stickers, fullTableOutput(flags.fullOutput))
		},
	}
	cmd.Flags().BoolVar(&favorites, "favorites", false, "only favorite stickers")
	cmd.Flags().BoolVar(&recent, "recent", false, "only recent stickers (recorded at pairing)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max number of stickers to return (0 = unlimited)")
	return cmd
}

func writeStickersList(dst io.Writer, stickers []store.Sticker, fullOutput bool) error {
	w := newTableWriter(dst)
	fmt.Fprintln(w, "FILE HASH\tLISTS\tTYPE\tSIZE\tWHEN\tDOWNLOADABLE")
	for _, s := range stickers {
		var lists []string
		when := ""
		if s.Favorite {
			lists = append(lists, "favorite")
			if s.FavoritedAt != nil {
				when = s.FavoritedAt.Local().Format("2006-01-02 15:04:05")
			}
		}
		if s.Recent {
			lists = append(lists, "recent")
			if when == "" && s.LastSentAt != nil {
				when = s.LastSentAt.Local().Format("2006-01-02 15:04:05")
			}
		}
		size := ""
		if s.Width > 0 && s.Height > 0 {
			size = fmt.Sprintf("%dx%d", s.Width, s.Height)
		}
		downloadable := "no"
		if s.Downloadable {
			downloadable = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			tableCell(s.FileHash, 16, fullOutput),
			strings.Join(lists, ","),
			stickerKind(s),
			size,
			when,
			downloadable,
		)
	}
	return w.Flush()
}

func stickerKind(s store.Sticker) string {
	switch {
	case s.IsAvatar:
		return "avatar"
	case s.IsLottie:
		return "lottie"
	default:
		return "image"
	}
}

type stickerDownloadResult struct {
	FileHash string `json:"file_hash"`
	Path     string `json:"path,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Source   string `json:"source,omitempty"`
	Error    string `json:"error,omitempty"`
}

type stickerDownloader func(ctx context.Context, info store.StickerDownloadInfo, target string) (int64, error)

func newStickersDownloadCmd(flags *rootFlags) *cobra.Command {
	var favorites, recent bool
	var hashes []string
	var outputDir string
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download favorite and recent stickers",
		Long: "Download stickers straight from WhatsApp's media servers. This needs neither\n" +
			"the store lock nor a connection, so it runs next to `sync --follow`. Files are\n" +
			"named by their SHA-256 and verified against it; files already present and\n" +
			"verified are not downloaded again.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.isReadOnly() && strings.TrimSpace(outputDir) == "" {
				return fmt.Errorf("--output is required in read-only mode")
			}
			ctx, cancel := withTimeout(cmd.Context(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			stickers, err := a.DB().ListStickerDownloads(store.ListStickersParams{Favorites: favorites, Recent: recent})
			if err != nil {
				return err
			}
			stickers, err = selectStickersByHash(stickers, hashes)
			if err != nil {
				return err
			}
			dir := strings.TrimSpace(outputDir)
			if dir == "" {
				dir = filepath.Join(a.StoreDir(), "media", "stickers")
			}
			results := downloadStickers(ctx, stickers, dir, downloadStickerDirect)
			return writeStickerDownloadResults(flags, results)
		},
	}
	cmd.Flags().BoolVar(&favorites, "favorites", false, "only favorite stickers")
	cmd.Flags().BoolVar(&recent, "recent", false, "only recent stickers (recorded at pairing)")
	cmd.Flags().StringArrayVar(&hashes, "hash", nil, "download only this file hash (repeatable)")
	cmd.Flags().StringVar(&outputDir, "output", "", "output directory (default: store media dir)")
	return cmd
}

func selectStickersByHash(stickers []store.StickerDownloadInfo, hashes []string) ([]store.StickerDownloadInfo, error) {
	if len(hashes) == 0 {
		return stickers, nil
	}
	byHash := make(map[string]store.StickerDownloadInfo, len(stickers))
	for _, s := range stickers {
		byHash[s.FileHash] = s
	}
	selected := make([]store.StickerDownloadInfo, 0, len(hashes))
	for _, hash := range hashes {
		s, ok := byHash[strings.TrimSpace(hash)]
		if !ok {
			return nil, fmt.Errorf("no listed sticker has file hash %q", hash)
		}
		selected = append(selected, s)
	}
	return selected, nil
}

func downloadStickers(ctx context.Context, stickers []store.StickerDownloadInfo, dir string, download stickerDownloader) []stickerDownloadResult {
	results := make([]stickerDownloadResult, 0, len(stickers))
	for _, info := range stickers {
		result := stickerDownloadResult{FileHash: info.FileHash}
		target := filepath.Join(dir, stickerFilename(info))
		if n, ok := stickerFileVerified(target, info.FileSHA256); ok {
			result.Path, result.Bytes, result.Source = target, n, "cache"
		} else if err := requireStickerMetadata(info); err != nil {
			result.Error = err.Error()
		} else if n, err := download(ctx, info, target); err != nil {
			result.Error = err.Error()
		} else {
			result.Path, result.Bytes, result.Source = target, n, "server"
		}
		results = append(results, result)
	}
	return results
}

func requireStickerMetadata(info store.StickerDownloadInfo) error {
	if len(info.FileSHA256) != sha256.Size {
		return fmt.Errorf("file hash is not a base64 SHA-256 digest, so a download could not be verified")
	}
	if info.DirectPath == "" || len(info.MediaKey) == 0 || len(info.FileEncSHA256) == 0 {
		return fmt.Errorf("sticker has no downloadable media metadata (run `wacli stickers refresh`)")
	}
	return nil
}

func downloadStickerDirect(ctx context.Context, info store.StickerDownloadInfo, target string) (int64, error) {
	return wa.DownloadMediaDirectToFile(ctx, info.DirectPath, info.FileEncSHA256, info.FileSHA256, info.MediaKey, info.FileLength, "sticker", target)
}

func stickerFilename(info store.StickerDownloadInfo) string {
	base := pathutil.SanitizeFilename(info.FileHash)
	if len(info.FileSHA256) == sha256.Size {
		base = hex.EncodeToString(info.FileSHA256)
	}
	return base + stickerExtension(info)
}

func stickerExtension(info store.StickerDownloadInfo) string {
	if info.IsLottie {
		return ".was"
	}
	if exts, err := mime.ExtensionsByType(info.MimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".webp"
}

func stickerFileVerified(path string, digest []byte) (int64, bool) {
	if len(digest) != sha256.Size {
		return 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || !bytes.Equal(h.Sum(nil), digest) {
		return 0, false
	}
	return n, true
}

func writeStickerDownloadResults(flags *rootFlags, results []stickerDownloadResult) error {
	var downloaded, cached, failed int
	for _, r := range results {
		switch {
		case r.Error != "":
			failed++
			message := fmt.Sprintf("sticker %s: %s", r.FileHash, r.Error)
			if flags.events {
				_ = out.NewEventWriter(os.Stderr, true).Emit("warning", map[string]any{
					"code": "sticker_download_failed", "message": message, "file_hash": r.FileHash, "error": r.Error,
				})
			} else {
				_ = out.WriteError(os.Stderr, false, fmt.Errorf("warning: %s", message))
			}
		case r.Source == "cache":
			cached++
		default:
			downloaded++
		}
	}
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"stickers":   results,
			"downloaded": downloaded,
			"cached":     cached,
			"failed":     failed,
		})
	}
	for _, r := range results {
		if r.Error == "" {
			fmt.Fprintf(os.Stdout, "%s (%d bytes, %s)\n", r.Path, r.Bytes, r.Source)
		}
	}
	fmt.Fprintf(os.Stdout, "Downloaded: %d  Cached: %d  Failed: %d\n", downloaded, cached, failed)
	return nil
}

func newStickersRefreshCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Fetch favorite stickers (live) and update local DB",
		Long: "Replay WhatsApp's synced app state in full, so favorites saved before this\n" +
			"store recorded them are stored. Favorites changed afterwards arrive during\n" +
			"sync without this command. To refresh from `sync --follow`, pass\n" +
			"`--refresh-stickers` to it instead.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd.Context(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			removePersistenceHandler, err := a.AddChatStatePersistenceHandler(ctx)
			if err != nil {
				return err
			}
			defer func() {
				a.WA().Disconnect()
				removePersistenceHandler()
			}()
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			if err := a.RefreshFavoriteStickers(ctx); err != nil {
				return err
			}

			favorites, err := a.DB().ListStickers(store.ListStickersParams{Favorites: true})
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"stickers": len(favorites)})
			}
			fmt.Fprintf(os.Stdout, "Imported %d favorite stickers.\n", len(favorites))
			return nil
		},
	}
}
