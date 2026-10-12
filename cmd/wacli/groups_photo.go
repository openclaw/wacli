package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
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
			imgBytes, err := readAsJPEG(file)
			if err != nil {
				return fmt.Errorf("read image: %w", err)
			}
			imgBytes, err = cropSquareJPEG(imgBytes)
			if err != nil {
				return fmt.Errorf("crop image: %w", err)
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

// cropSquareJPEG center-crops a JPEG to a square, since WhatsApp shows group
// pictures as squares and rejects some other shapes.
func cropSquareJPEG(data []byte) ([]byte, error) {
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	if b.Dx() == side && b.Dy() == side {
		return data, nil
	}
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), src, image.Pt(x0, y0), draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
