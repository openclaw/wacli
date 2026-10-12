package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newGroupsPhotoCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "photo",
		Short: "Set or remove a group or community picture",
	}
	cmd.AddCommand(newGroupsPhotoSetCmd(flags))
	cmd.AddCommand(newGroupsPhotoRemoveCmd(flags))
	return cmd
}

func newGroupsPhotoSetCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	var file string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set the group picture (JPEG or PNG, center-cropped to a square and resized to <=640px)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" || strings.TrimSpace(file) == "" {
				return fmt.Errorf("--jid and --file are required")
			}
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			imgBytes, err := readAsSquareJPEG(file)
			if err != nil {
				return fmt.Errorf("read image: %w", err)
			}
			return runGroupsPhoto(flags, gjid.String(), imgBytes)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group or community JID (…@g.us)")
	cmd.Flags().StringVar(&file, "file", "", "image file (JPEG or PNG)")
	return cmd
}

func newGroupsPhotoRemoveCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove the group picture",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
			}
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			return runGroupsPhoto(flags, gjid.String(), nil)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group or community JID (…@g.us)")
	return cmd
}

func runGroupsPhoto(flags *rootFlags, jidStr string, avatar []byte) error {
	gjid, err := parseGroupJID(jidStr)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		return err
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	pictureID, err := a.WA().SetGroupPhoto(ctx, gjid, avatar)
	if err != nil {
		return fmt.Errorf("set group photo: %w", err)
	}
	removed := avatar == nil
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"jid": gjid.String(), "picture_id": pictureID, "removed": removed})
	}
	if removed {
		fmt.Fprintln(os.Stdout, "Group picture removed.")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Group picture updated (id: %s)\n", pictureID)
	return nil
}

// readAsSquareJPEG reads a JPEG or PNG, center-crops it to a square (WhatsApp
// shows group pictures as squares) before resizing it to at most 640px, so a
// large rectangular photo keeps its resolution, and encodes it as JPEG.
func readAsSquareJPEG(path string) ([]byte, error) {
	data, err := readRegularFileLimited(path, profileMaxInputBytes)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unsupported image format: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > profileMaxInputPixels/cfg.Height {
		return nil, fmt.Errorf("image dimensions are too large")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unsupported image format: %w", err)
	}
	img = resizeIfNeeded(cropSquare(img), profileMaxPx)

	// Composite onto white background to flatten any alpha channel.
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// cropSquare returns the centered square of src.
func cropSquare(src image.Image) image.Image {
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	if b.Dx() == side && b.Dy() == side {
		return src
	}
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), src, image.Pt(x0, y0), draw.Src)
	return dst
}
