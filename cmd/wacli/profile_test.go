package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
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
