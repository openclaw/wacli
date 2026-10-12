package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestCropSquareJPEGCentersAndSquares(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 300, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 300; x++ {
			src.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := cropSquareJPEG(buf.Bytes())
	if err != nil {
		t.Fatalf("cropSquareJPEG: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 120 || b.Dy() != 120 {
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
