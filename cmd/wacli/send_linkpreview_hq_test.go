package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
)

type fakeLPUploader struct {
	got  []byte
	typ  whatsmeow.MediaType
	fail bool
}

func (f *fakeLPUploader) Upload(_ context.Context, data []byte, mt whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	if f.fail {
		return whatsmeow.UploadResponse{}, errors.New("boom")
	}
	f.got, f.typ = data, mt
	return whatsmeow.UploadResponse{DirectPath: "/v/t62/x", MediaKey: []byte("k"), FileSHA256: []byte("s"), FileEncSHA256: []byte("e")}, nil
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestApplyLinkPreviewImageUploadsHQThumbnail(t *testing.T) {
	up := &fakeLPUploader{}
	ext := &waProto.ExtendedTextMessage{}
	applyLinkPreviewImage(context.Background(), up, ext, testPNG(t, 2400, 1260))
	if up.typ != whatsmeow.MediaLinkThumbnail {
		t.Fatalf("media type = %q", up.typ)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(up.got))
	if err != nil {
		t.Fatalf("uploaded not jpeg: %v", err)
	}
	if ext.GetThumbnailWidth() != uint32(cfg.Width) || ext.GetThumbnailHeight() != uint32(cfg.Height) || cfg.Width > linkPreviewHQMaxDimension {
		t.Fatalf("dims %dx%d vs %dx%d", ext.GetThumbnailWidth(), ext.GetThumbnailHeight(), cfg.Width, cfg.Height)
	}
	t.Logf("HQ %dx%d %d bytes, inline %d bytes", cfg.Width, cfg.Height, len(up.got), len(ext.GetJPEGThumbnail()))
	if ext.GetThumbnailDirectPath() != "/v/t62/x" || string(ext.GetMediaKey()) != "k" || string(ext.GetThumbnailSHA256()) != "s" || string(ext.GetThumbnailEncSHA256()) != "e" || ext.GetMediaKeyTimestamp() == 0 {
		t.Fatalf("upload fields not set: %+v", ext)
	}
	small, err := jpeg.DecodeConfig(bytes.NewReader(ext.GetJPEGThumbnail()))
	if err != nil || small.Width > linkPreviewInlineMaxDimension {
		t.Fatalf("inline thumb: %v %+v", err, small)
	}
	if ext.GetPreviewType() != waProto.ExtendedTextMessage_IMAGE {
		t.Fatal("preview type")
	}
}

func TestApplyLinkPreviewImageUploadFailureFallsBack(t *testing.T) {
	ext := &waProto.ExtendedTextMessage{}
	warning := applyLinkPreviewImage(context.Background(), &fakeLPUploader{fail: true}, ext, testPNG(t, 800, 400))
	if ext.ThumbnailDirectPath != nil || len(ext.GetJPEGThumbnail()) == 0 {
		t.Fatalf("fallback wrong: %+v", ext)
	}
	if !strings.Contains(warning, "HQ thumbnail upload failed") || !strings.Contains(warning, "boom") {
		t.Fatalf("warning = %q", warning)
	}
}

func TestApplyLinkPreviewImageUndecodableKeepsRaw(t *testing.T) {
	ext := &waProto.ExtendedTextMessage{JPEGThumbnail: []byte("jpeg")}
	applyLinkPreviewImage(context.Background(), &fakeLPUploader{}, ext, []byte("jpeg"))
	if string(ext.GetJPEGThumbnail()) != "jpeg" || ext.ThumbnailDirectPath != nil {
		t.Fatal("should be untouched")
	}
}

func TestApplyLinkPreviewImageDropsLargeUndecodable(t *testing.T) {
	raw := bytes.Repeat([]byte{0x52}, linkPreviewMaxInlineRawBytes+1)
	ext := &waProto.ExtendedTextMessage{JPEGThumbnail: raw}
	up := &fakeLPUploader{}
	applyLinkPreviewImage(context.Background(), up, ext, raw)
	if len(ext.GetJPEGThumbnail()) != 0 || up.got != nil {
		t.Fatalf("oversized undecodable thumbnail should be dropped without upload")
	}
}

func TestApplyLinkPreviewImageWithoutUploaderKeepsSmallPreview(t *testing.T) {
	ext := &waProto.ExtendedTextMessage{}
	applyLinkPreviewImage(context.Background(), nil, ext, testPNG(t, 1200, 630))
	if ext.ThumbnailDirectPath != nil || len(ext.GetJPEGThumbnail()) == 0 {
		t.Fatalf("expected inline-only preview: %+v", ext)
	}
}

func TestResizeToJPEGRejectsOversizedDimensions(t *testing.T) {
	// A valid PNG header claiming 5000x5000 (25 MP) must be rejected before decoding pixels.
	var buf bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, 5000, 5000))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 5<<20 {
		t.Fatalf("test image unexpectedly large: %d", buf.Len())
	}
	if _, _, _, err := resizeToJPEG(buf.Bytes(), linkPreviewHQMaxDimension, 85); err == nil {
		t.Fatal("expected oversized image to be rejected")
	}
}

func TestApplyLinkPreviewSmallModeDoesNotUpload(t *testing.T) {
	t.Setenv("WACLI_LINKPREVIEW_SMALL", "1")
	up := &fakeLPUploader{}
	ext := &waProto.ExtendedTextMessage{}
	warning := applyLinkPreviewImage(context.Background(), up, ext, testPNG(t, 800, 400))
	if warning != "" || up.got != nil || ext.ThumbnailDirectPath != nil || len(ext.GetJPEGThumbnail()) == 0 {
		t.Fatalf("warning=%q uploaded=%t ext=%v", warning, up.got != nil, ext)
	}
}
