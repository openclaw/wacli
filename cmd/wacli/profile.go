package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	stdraw "image/draw"
	"image/jpeg"
	_ "image/png"
	"os"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/out"
)

// profileMaxPx is the max dimension WhatsApp accepts for profile pictures.
const profileMaxPx = 640

func newProfileCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage your WhatsApp profile",
	}
	cmd.AddCommand(newProfileSetPictureCmd(flags))
	return cmd
}

func newProfileSetPictureCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-picture <image>",
		Short: "Set your WhatsApp profile picture (JPEG or PNG, auto-resized to <=640px)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}

			imgBytes, err := readAsJPEG(args[0])
			if err != nil {
				return fmt.Errorf("read image: %w", err)
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

			pictureID, err := a.WA().SetProfilePicture(ctx, imgBytes)
			if err != nil {
				return fmt.Errorf("set profile picture: %w", err)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"picture_id": pictureID})
			}
			fmt.Fprintf(os.Stdout, "Profile picture updated (id: %s)\n", pictureID)
			return nil
		},
	}
	return cmd
}

// readAsJPEG reads the file at path, decodes it, resizes to <=profileMaxPx if
// needed, and returns JPEG-encoded bytes suitable for WhatsApp.
func readAsJPEG(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unsupported image format: %w", err)
	}

	img = resizeIfNeeded(img, profileMaxPx)

	// Composite onto white background to flatten any alpha channel.
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	stdraw.Draw(rgba, bounds, &image.Uniform{color.White}, image.Point{}, stdraw.Src)
	stdraw.Draw(rgba, bounds, img, bounds.Min, stdraw.Over)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// resizeIfNeeded returns a nearest-neighbour scaled copy of src when either
// dimension exceeds maxPx, otherwise returns src unchanged.
func resizeIfNeeded(src image.Image, maxPx int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxPx && h <= maxPx {
		return src
	}
	larger := w
	if h > larger {
		larger = h
	}
	nw := w * maxPx / larger
	nh := h * maxPx / larger
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	scaleX := float64(w) / float64(nw)
	scaleY := float64(h) / float64(nh)
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			srcX := b.Min.X + int(float64(x)*scaleX)
			srcY := b.Min.Y + int(float64(y)*scaleY)
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}
