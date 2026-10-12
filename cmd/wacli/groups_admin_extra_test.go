package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestReadAsSquareJPEGCropsBeforeResizing(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 1600; x++ {
			src.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	path := filepath.Join(t.TempDir(), "wide.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(f, src); err != nil {
		t.Fatalf("encode: %v", err)
	}
	f.Close()
	got, err := readAsSquareJPEG(path)
	if err != nil {
		t.Fatalf("readAsSquareJPEG: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.Width != 640 || cfg.Height != 640 {
		t.Fatalf("expected 640x640, got %dx%d", cfg.Width, cfg.Height)
	}
}

func TestCropSquareKeepsTheCenter(t *testing.T) {
	// Three 100px bands: red, green, blue. The centered square is the green band.
	src := image.NewRGBA(image.Rect(0, 0, 300, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 300; x++ {
			c := color.RGBA{G: 255, A: 255}
			if x < 100 {
				c = color.RGBA{R: 255, A: 255}
			} else if x >= 200 {
				c = color.RGBA{B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}
	got := cropSquare(src)
	b := got.Bounds()
	if b.Dx() != 100 || b.Dy() != 100 {
		t.Fatalf("expected 100x100, got %dx%d", b.Dx(), b.Dy())
	}
	for _, p := range []image.Point{{b.Min.X, b.Min.Y}, {b.Max.X - 1, b.Max.Y - 1}, {b.Min.X + 50, b.Min.Y + 50}} {
		if r, g, bl, _ := got.At(p.X, p.Y).RGBA(); r != 0 || bl != 0 || g == 0 {
			t.Fatalf("pixel %v is not from the center band: r=%d g=%d b=%d", p, r, g, bl)
		}
	}
}

func TestGroupsCommandExposesAdminSubcommands(t *testing.T) {
	cmd := newGroupsCmd(&rootFlags{})
	for _, path := range [][]string{{"photo", "set"}, {"photo", "remove"}, {"join-approval"}, {"admin-add"}, {"community", "groups"}, {"community", "link"}, {"community", "unlink"}} {
		found, rest, err := cmd.Find(path)
		if err != nil || len(rest) != 0 || found.Name() != path[len(path)-1] || !found.Runnable() {
			t.Fatalf("groups %v: found %q, rest %v, err %v", path, found.Name(), rest, err)
		}
	}
}

func TestCommunityGroupEntriesSkipsNil(t *testing.T) {
	jid := types.NewJID("123", types.GroupServer)
	got := communityGroupEntries([]*types.GroupLinkTarget{nil, {JID: jid, GroupName: types.GroupName{Name: "General"}, GroupIsDefaultSub: types.GroupIsDefaultSub{IsDefaultSubGroup: true}}})
	if len(got) != 1 || got[0].JID != jid.String() || got[0].Name != "General" || !got[0].DefaultGroup {
		t.Fatalf("unexpected entries: %+v", got)
	}
}
