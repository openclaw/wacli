package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
)

const (
	pictureTypePreview = "preview"
	pictureTypeImage   = "image"
)

// pictureMaxBytes caps the JPEG download to defend against a misbehaving CDN.
// WhatsApp full-size pictures are <=640px JPEG, so 5 MiB is comfortably above
// any reasonable real value.
const pictureMaxBytes = 5 * 1024 * 1024

// httpDoer is the subset of *http.Client used by the picture downloader; kept
// narrow so tests can inject a fake without spinning up a server.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func newContactsGetPictureCmd(flags *rootFlags) *cobra.Command {
	var targetRaw, outputPath, pictureType, existingID string
	cmd := &cobra.Command{
		Use:   "get-picture",
		Short: "Download a contact's WhatsApp profile picture to a file",
		Long: `Download a contact's WhatsApp profile picture as a JPEG.

Fetches metadata via whatsmeow's GetProfilePictureInfo and then downloads the
JPEG bytes from WhatsApp's CDN. Useful for syncing avatars into local address
books for contacts whose chat is archived (and therefore not cached by the
macOS WhatsApp desktop app).

Use --type preview for the 96x96 thumbnail (default) or --type image for the
full-size picture (up to 640px). Use --output - to stream bytes to stdout;
--json + --output - is rejected because both write to stdout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			preview, err := parsePictureType(pictureType)
			if err != nil {
				return err
			}
			if strings.TrimSpace(outputPath) == "" {
				return fmt.Errorf("--output is required")
			}
			if flags.asJSON && outputPath == "-" {
				return fmt.Errorf("--json cannot be combined with --output -")
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
				return wrapProfilePictureError(err)
			}
			if info == nil {
				return fmt.Errorf("profile picture is unchanged (matches --existing-id %q)", existingID)
			}
			if info.URL == "" {
				return fmt.Errorf("no profile picture available")
			}

			data, err := downloadProfilePicture(ctx, http.DefaultClient, info.URL)
			if err != nil {
				return err
			}

			resolved, written, err := writeProfilePicture(outputPath, data)
			if err != nil {
				return err
			}

			result := contactsGetPictureOutput{
				JID:    target.String(),
				ID:     info.ID,
				Type:   info.Type,
				URL:    info.URL,
				Output: resolved,
				Bytes:  written,
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, result)
			}
			if resolved == "-" {
				return nil // bytes already on stdout
			}
			fmt.Fprintf(os.Stdout, "%s (%d bytes)\n", resolved, written)
			return nil
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "target JID or phone number")
	cmd.Flags().StringVar(&outputPath, "output", "", "output file path (use - for stdout)")
	cmd.Flags().StringVar(&pictureType, "type", pictureTypePreview, "preview (96x96 thumbnail) or image (full-size, up to 640px)")
	cmd.Flags().StringVar(&existingID, "existing-id", "", "skip download if picture ID matches")
	return cmd
}

// parsePictureType maps the --type flag to the whatsmeow `preview bool` arg.
// preview = true → 96x96 thumbnail; preview = false → up to 640px full-size.
func parsePictureType(s string) (preview bool, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case pictureTypePreview, "":
		return true, nil
	case pictureTypeImage, "full":
		return false, nil
	default:
		return false, fmt.Errorf("--type must be one of: preview, image (got %q)", s)
	}
}

// wrapProfilePictureError converts whatsmeow's profile-picture sentinel errors
// into the user-facing strings promised by the command spec.
func wrapProfilePictureError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		return fmt.Errorf("no profile picture available")
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return fmt.Errorf("not authorized to view this profile picture")
	default:
		return fmt.Errorf("get profile picture info: %w", err)
	}
}

// downloadProfilePicture GETs the WhatsApp CDN URL and returns the body bytes,
// rejecting any response larger than pictureMaxBytes.
func downloadProfilePicture(ctx context.Context, client httpDoer, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build profile picture request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download profile picture: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("download profile picture: HTTP %d", resp.StatusCode)
	}
	body := io.LimitReader(resp.Body, pictureMaxBytes+1)
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("download profile picture: %w", err)
	}
	if int64(len(data)) > pictureMaxBytes {
		return nil, fmt.Errorf("download profile picture: response exceeds %d bytes", pictureMaxBytes)
	}
	return data, nil
}

// writeProfilePicture writes data to outputPath (or stdout if "-") and returns
// the resolved path string used for output messages plus the number of bytes
// written. Non-stdout writes use 0600 to mirror wacli's owner-only convention.
func writeProfilePicture(outputPath string, data []byte) (string, int, error) {
	if outputPath == "-" {
		n, err := os.Stdout.Write(data)
		if err != nil {
			return "-", n, fmt.Errorf("write stdout: %w", err)
		}
		return "-", n, nil
	}
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		return "", 0, fmt.Errorf("write %s: %w", outputPath, err)
	}
	abs, err := filepath.Abs(outputPath)
	if err != nil {
		abs = outputPath
	}
	return abs, len(data), nil
}

type contactsGetPictureOutput struct {
	JID    string `json:"jid"`
	ID     string `json:"id,omitempty"`
	Type   string `json:"type,omitempty"`
	URL    string `json:"url,omitempty"`
	Output string `json:"output"`
	Bytes  int    `json:"bytes"`
}
