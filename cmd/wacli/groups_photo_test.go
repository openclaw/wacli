package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
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

func TestCropSquareKeepsCenteredSquare(t *testing.T) {
	got := cropSquare(image.NewRGBA(image.Rect(0, 0, 300, 120)))
	if b := got.Bounds(); b.Dx() != 120 || b.Dy() != 120 {
		t.Fatalf("expected 120x120, got %dx%d", b.Dx(), b.Dy())
	}
}

func TestGroupsCommandExposesPhotoSubcommands(t *testing.T) {
	cmd := newGroupsCmd(&rootFlags{})
	for _, path := range [][]string{{"photo", "set"}, {"photo", "remove"}} {
		if _, _, err := cmd.Find(path); err != nil {
			t.Fatalf("missing groups %v command: %v", path, err)
		}
	}
}
