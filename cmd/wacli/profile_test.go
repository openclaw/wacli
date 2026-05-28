package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func TestReadAsJPEGResizesAndFlattensAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < src.Bounds().Dy(); y++ {
		for x := 0; x < src.Bounds().Dx(); x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 255, A: 0})
		}
	}
	src.SetNRGBA(799, 399, color.NRGBA{R: 200, G: 20, B: 20, A: 255})

	path := filepath.Join(t.TempDir(), "avatar.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, src); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := readAsJPEG(path)
	if err != nil {
		t.Fatalf("readAsJPEG: %v", err)
	}

	out, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode output JPEG: %v", err)
	}
	if got := out.Bounds().Size(); got.X != profileMaxPx || got.Y != 320 {
		t.Fatalf("size = %dx%d, want %dx320", got.X, got.Y, profileMaxPx)
	}
	r, g, b, _ := out.At(0, 0).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatalf("transparent pixel was not flattened onto white, got rgb=(%d,%d,%d)", r>>8, g>>8, b>>8)
	}
}

func TestResizeIfNeededKeepsSmallImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	if got := resizeIfNeeded(img, profileMaxPx); got != img {
		t.Fatal("resizeIfNeeded should return small images unchanged")
	}
}

func TestProfileCommandExposesProfileManagementSubcommands(t *testing.T) {
	cmd := newProfileCmd(&rootFlags{})
	for _, name := range []string{"set-picture", "remove-picture", "picture-info", "set-about", "get-about", "set-name", "business"} {
		if _, _, err := cmd.Find([]string{name}); err != nil {
			t.Fatalf("missing profile %s command: %v", name, err)
		}
	}
}

func TestProfileReadCommandsExposeTargetFlags(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
	}{
		{name: "picture-info", cmd: newProfilePictureInfoCmd(&rootFlags{})},
		{name: "get-about", cmd: newProfileGetAboutCmd(&rootFlags{})},
		{name: "business", cmd: newProfileBusinessCmd(&rootFlags{})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cmd.Flags().Lookup("jid") == nil {
				t.Fatalf("missing --jid flag")
			}
		})
	}
}

func TestProfilePictureInfoJSONOmitsHashBytesWhenEmpty(t *testing.T) {
	info := profilePictureInfoOutput{
		JID:        "15551234567@s.whatsapp.net",
		ID:         "abc",
		URL:        "https://example.invalid/avatar.jpg",
		Type:       "image",
		DirectPath: "/mms/path",
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(data, []byte("hash")) {
		t.Fatalf("empty hash should be omitted: %s", data)
	}
}

func TestFetchProfileAboutReturnsTargetStatus(t *testing.T) {
	target := types.NewJID("15551234567", types.DefaultUserServer)
	client := fakeProfileClient{
		userInfo: map[types.JID]types.UserInfo{
			target: {Status: "Available"},
		},
	}
	got, err := fetchProfileAbout(context.Background(), client, target)
	if err != nil {
		t.Fatalf("fetchProfileAbout: %v", err)
	}
	if got.JID != target.String() || got.About != "Available" {
		t.Fatalf("unexpected output: %+v", got)
	}
}

func TestFetchProfileAboutErrorsWhenTargetMissing(t *testing.T) {
	target := types.NewJID("15551234567", types.DefaultUserServer)
	_, err := fetchProfileAbout(context.Background(), fakeProfileClient{userInfo: map[types.JID]types.UserInfo{}}, target)
	if err == nil {
		t.Fatal("expected missing target error")
	}
}

type fakeProfileClient struct {
	userInfo map[types.JID]types.UserInfo
}

func (f fakeProfileClient) GetUserInfo(_ context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error) {
	if len(jids) != 1 {
		return nil, nil
	}
	return f.userInfo, nil
}
