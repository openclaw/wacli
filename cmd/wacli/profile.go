package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	stdraw "image/draw"
	"image/jpeg"
	_ "image/png"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

// profileMaxPx is the max dimension WhatsApp accepts for profile pictures.
const profileMaxPx = 640
const profileMaxInputBytes = 20 * 1024 * 1024
const profileMaxInputPixels = 40_000_000

func newProfileCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage your WhatsApp profile",
	}
	cmd.AddCommand(newProfileSetPictureCmd(flags))
	cmd.AddCommand(newProfileRemovePictureCmd(flags))
	cmd.AddCommand(newProfilePictureInfoCmd(flags))
	cmd.AddCommand(newProfileSetAboutCmd(flags))
	cmd.AddCommand(newProfileGetAboutCmd(flags))
	cmd.AddCommand(newProfileSetNameCmd(flags))
	cmd.AddCommand(newProfileBusinessCmd(flags))
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

func newProfileRemovePictureCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove-picture",
		Short: "Remove your WhatsApp profile picture",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel, a, lk, err := openLiveProfileApp(flags, true)
			if err != nil {
				return err
			}
			defer cancel()
			defer closeApp(a, lk)
			pictureID, err := a.WA().SetProfilePicture(ctx, nil)
			if err != nil {
				return fmt.Errorf("remove profile picture: %w", err)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"removed": true, "picture_id": pictureID})
			}
			fmt.Fprintln(os.Stdout, "Profile picture removed.")
			return nil
		},
	}
	return cmd
}

func newProfileSetAboutCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-about <text>",
		Short: "Set your WhatsApp profile About text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			about := strings.TrimSpace(args[0])
			if about == "" {
				return fmt.Errorf("about text is required")
			}
			ctx, cancel, a, lk, err := openLiveProfileApp(flags, true)
			if err != nil {
				return err
			}
			defer cancel()
			defer closeApp(a, lk)
			if err := a.WA().SetStatusMessage(ctx, about); err != nil {
				return fmt.Errorf("set profile about: %w", err)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"about": about})
			}
			fmt.Fprintln(os.Stdout, "Profile About updated.")
			return nil
		},
	}
	return cmd
}

func newProfileSetNameCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-name <name>",
		Short: "Set your WhatsApp profile display name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("profile name is required")
			}
			ctx, cancel, a, lk, err := openLiveProfileApp(flags, true)
			if err != nil {
				return err
			}
			defer cancel()
			defer closeApp(a, lk)
			if err := a.WA().SetProfileName(ctx, name); err != nil {
				return fmt.Errorf("set profile name: %w", err)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"name": name})
			}
			fmt.Fprintln(os.Stdout, "Profile name updated.")
			return nil
		},
	}
	return cmd
}

func newProfilePictureInfoCmd(flags *rootFlags) *cobra.Command {
	var targetRaw, existingID string
	var preview bool
	cmd := &cobra.Command{
		Use:   "picture-info --jid <jid-or-phone>",
		Short: "Fetch WhatsApp profile picture metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			target, err := parseProfileTarget(targetRaw)
			if err != nil {
				return err
			}
			ctx, cancel, a, lk, err := openLiveProfileApp(flags, true)
			if err != nil {
				return err
			}
			defer cancel()
			defer closeApp(a, lk)
			info, err := a.WA().GetProfilePictureInfo(ctx, target, preview, existingID)
			if err != nil {
				return fmt.Errorf("get profile picture info: %w", err)
			}
			output := formatProfilePictureInfo(target, info)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, output)
			}
			if info == nil {
				fmt.Fprintf(os.Stdout, "%s profile picture is unchanged.\n", sanitize(target.String()))
				return nil
			}
			fmt.Fprintf(os.Stdout, "JID: %s\nID: %s\nType: %s\nURL: %s\nDirect path: %s\n", sanitize(output.JID), sanitize(output.ID), sanitize(output.Type), sanitize(output.URL), sanitize(output.DirectPath))
			return nil
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "target JID or phone number")
	cmd.Flags().BoolVar(&preview, "preview", false, "fetch preview picture metadata")
	cmd.Flags().StringVar(&existingID, "existing-id", "", "last known picture ID; unchanged pictures return null metadata")
	return cmd
}

func newProfileGetAboutCmd(flags *rootFlags) *cobra.Command {
	var targetRaw string
	cmd := &cobra.Command{
		Use:   "get-about --jid <jid-or-phone>",
		Short: "Fetch a user's WhatsApp profile About text",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			target, err := parseProfileTarget(targetRaw)
			if err != nil {
				return err
			}
			ctx, cancel, a, lk, err := openLiveProfileApp(flags, true)
			if err != nil {
				return err
			}
			defer cancel()
			defer closeApp(a, lk)
			output, err := fetchProfileAbout(ctx, a.WA(), target)
			if err != nil {
				return fmt.Errorf("get profile about: %w", err)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, output)
			}
			fmt.Fprintf(os.Stdout, "JID: %s\nAbout: %s\n", sanitize(output.JID), sanitize(output.About))
			return nil
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "target JID or phone number")
	return cmd
}

func newProfileBusinessCmd(flags *rootFlags) *cobra.Command {
	var targetRaw string
	cmd := &cobra.Command{
		Use:   "business --jid <jid-or-phone>",
		Short: "Fetch a WhatsApp business profile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			target, err := parseProfileTarget(targetRaw)
			if err != nil {
				return err
			}
			ctx, cancel, a, lk, err := openLiveProfileApp(flags, true)
			if err != nil {
				return err
			}
			defer cancel()
			defer closeApp(a, lk)
			profile, err := a.WA().GetBusinessProfile(ctx, target)
			if err != nil {
				return fmt.Errorf("get business profile: %w", err)
			}
			output := formatBusinessProfile(profile)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, output)
			}
			fmt.Fprintf(os.Stdout, "JID: %s\nAddress: %s\nEmail: %s\nTimezone: %s\n", sanitize(output.JID), sanitize(output.Address), sanitize(output.Email), sanitize(output.BusinessHoursTimeZone))
			return nil
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "target JID or phone number")
	return cmd
}

func openLiveProfileApp(flags *rootFlags, needLock bool) (context.Context, context.CancelFunc, *app.App, *lock.Lock, error) {
	ctx, cancel := withTimeout(context.Background(), flags)
	a, lk, err := newApp(ctx, flags, needLock, false)
	if err != nil {
		cancel()
		return nil, nil, nil, nil, err
	}
	if err := a.EnsureAuthed(); err != nil {
		cancel()
		closeApp(a, lk)
		return nil, nil, nil, nil, err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		cancel()
		closeApp(a, lk)
		return nil, nil, nil, nil, err
	}
	return ctx, cancel, a, lk, nil
}

func parseProfileTarget(raw string) (types.JID, error) {
	if strings.TrimSpace(raw) == "" {
		return types.JID{}, fmt.Errorf("--jid is required")
	}
	return wa.ParseUserOrJID(raw)
}

type profilePictureInfoOutput struct {
	JID        string `json:"jid"`
	ID         string `json:"id,omitempty"`
	URL        string `json:"url,omitempty"`
	Type       string `json:"type,omitempty"`
	DirectPath string `json:"direct_path,omitempty"`
	Hash       string `json:"hash,omitempty"`
	Unchanged  bool   `json:"unchanged,omitempty"`
}

func formatProfilePictureInfo(jid types.JID, info *types.ProfilePictureInfo) profilePictureInfoOutput {
	output := profilePictureInfoOutput{JID: jid.String()}
	if info == nil {
		output.Unchanged = true
		return output
	}
	output.ID = info.ID
	output.URL = info.URL
	output.Type = info.Type
	output.DirectPath = info.DirectPath
	if len(info.Hash) > 0 {
		output.Hash = base64.StdEncoding.EncodeToString(info.Hash)
	}
	return output
}

type profileAboutOutput struct {
	JID   string `json:"jid"`
	About string `json:"about"`
}

type profileUserInfoGetter interface {
	GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error)
}

func fetchProfileAbout(ctx context.Context, client profileUserInfoGetter, target types.JID) (profileAboutOutput, error) {
	infos, err := client.GetUserInfo(ctx, []types.JID{target})
	if err != nil {
		return profileAboutOutput{}, err
	}
	info, ok := infos[target]
	if !ok {
		return profileAboutOutput{}, fmt.Errorf("profile about unavailable for %s", target.String())
	}
	return profileAboutOutput{JID: target.String(), About: info.Status}, nil
}

type businessProfileOutput struct {
	JID                   string                      `json:"jid"`
	Address               string                      `json:"address,omitempty"`
	Email                 string                      `json:"email,omitempty"`
	Categories            []types.Category            `json:"categories,omitempty"`
	ProfileOptions        map[string]string           `json:"profile_options,omitempty"`
	BusinessHoursTimeZone string                      `json:"business_hours_timezone,omitempty"`
	BusinessHours         []types.BusinessHoursConfig `json:"business_hours,omitempty"`
}

func formatBusinessProfile(profile *types.BusinessProfile) businessProfileOutput {
	if profile == nil {
		return businessProfileOutput{}
	}
	return businessProfileOutput{
		JID:                   profile.JID.String(),
		Address:               profile.Address,
		Email:                 profile.Email,
		Categories:            profile.Categories,
		ProfileOptions:        profile.ProfileOptions,
		BusinessHoursTimeZone: profile.BusinessHoursTimeZone,
		BusinessHours:         profile.BusinessHours,
	}
}

// readAsJPEG reads the file at path, decodes it, resizes to <=profileMaxPx if
// needed, and returns JPEG-encoded bytes suitable for WhatsApp.
func readAsJPEG(path string) ([]byte, error) {
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
