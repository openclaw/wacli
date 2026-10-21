package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"time"

	"github.com/openclaw/wacli/internal/linkpreview"
	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Official clients render the large, full-width link preview only when the
// ExtendedTextMessage references an uploaded high-resolution thumbnail
// (ThumbnailDirectPath/SHA256/EncSHA256 + MediaKey, MediaType "WhatsApp Link
// Thumbnail Keys") with ThumbnailWidth/Height. JPEGThumbnail alone is drawn as
// the small square thumbnail next to the title.
const (
	linkPreviewHQMaxDimension     = 1200
	linkPreviewInlineMaxDimension = 140
	linkPreviewMinLargeWidth      = 300
	// linkPreviewMaxInlineRawBytes caps an og:image we cannot decode (e.g.
	// WebP) that would otherwise be embedded verbatim as JPEGThumbnail.
	linkPreviewMaxInlineRawBytes = 300 << 10
	// linkPreviewMaxSourcePixels bounds decode memory: the header is checked
	// before the full decode, so a small compressed file cannot expand into a
	// huge bitmap (16 MP is ~64 MB as 8-bit RGBA).
	linkPreviewMaxSourcePixels = 16_000_000
)

var errLinkPreviewDimensions = errors.New("image dimensions exceed preview limit")

type linkPreviewUploader interface {
	Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
}

// resizeToJPEG decodes data, scales it to fit maxDim and re-encodes as JPEG.
func resizeToJPEG(data []byte, maxDim, quality int) ([]byte, int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > linkPreviewMaxSourcePixels {
		return nil, 0, 0, fmt.Errorf("%w: %dx%d", errLinkPreviewDimensions, cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, 0, 0, fmt.Errorf("invalid image dimensions: %dx%d", srcW, srcH)
	}
	dstW, dstH := scaledDimensions(srcW, srcH, maxDim)
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	// box-average downscale for decent quality
	for y := 0; y < dstH; y++ {
		y0 := b.Min.Y + y*srcH/dstH
		y1 := b.Min.Y + (y+1)*srcH/dstH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dstW; x++ {
			x0 := b.Min.X + x*srcW/dstW
			x1 := b.Min.X + (x+1)*srcW/dstW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, n uint64
			stepY := (y1-y0)/4 + 1
			stepX := (x1-x0)/4 + 1
			for sy := y0; sy < y1; sy += stepY {
				for sx := x0; sx < x1; sx += stepX {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					// composite on white
					r += uint64(cr + (0xffff - ca))
					g += uint64(cg + (0xffff - ca))
					bl += uint64(cb + (0xffff - ca))
					n++
				}
			}
			dst.Set(x, y, color.RGBA64{uint16(r / n), uint16(g / n), uint16(bl / n), 0xffff})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, 0, 0, err
	}
	return out.Bytes(), dstW, dstH, nil
}

// applyLinkPreviewImage turns the raw og:image into a WhatsApp-style preview:
// a small inline JPEGThumbnail always, plus an uploaded HQ thumbnail (large
// banner preview) when an uploader is available. Errors degrade gracefully.
// A failed upload is returned as a warning instead of printed: delegated sends
// run here inside `sync --follow`, whose stderr may be an NDJSON event stream,
// so only the output-owning caller may render it.
func applyLinkPreviewImage(ctx context.Context, up linkPreviewUploader, ext *waProto.ExtendedTextMessage, raw []byte) (warning string) {
	if ext == nil || len(raw) == 0 {
		return ""
	}
	small, _, _, err := resizeToJPEG(raw, linkPreviewInlineMaxDimension, 75)
	if err != nil {
		// Undecodable: keep the raw bytes attachLinkPreview set only while
		// they stay within the previous inline size limit.
		if len(raw) > linkPreviewMaxInlineRawBytes || errors.Is(err, errLinkPreviewDimensions) {
			ext.JPEGThumbnail = nil
		}
		return ""
	}
	ext.JPEGThumbnail = small
	ext.PreviewType = waProto.ExtendedTextMessage_IMAGE.Enum()
	if up == nil || linkpreview.SmallThumbnails() {
		return ""
	}
	hq, w, h, err := resizeToJPEG(raw, linkPreviewHQMaxDimension, 85)
	if err != nil || w < linkPreviewMinLargeWidth {
		return ""
	}
	upCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := up.Upload(upCtx, hq, whatsmeow.MediaLinkThumbnail)
	if err != nil {
		return fmt.Sprintf("link preview HQ thumbnail upload failed, using small preview: %v", err)
	}
	ext.ThumbnailDirectPath = proto.String(resp.DirectPath)
	ext.ThumbnailSHA256 = resp.FileSHA256
	ext.ThumbnailEncSHA256 = resp.FileEncSHA256
	ext.MediaKey = resp.MediaKey
	ext.MediaKeyTimestamp = proto.Int64(time.Now().Unix())
	ext.ThumbnailWidth = proto.Uint32(uint32(w))
	ext.ThumbnailHeight = proto.Uint32(uint32(h))
	return ""
}

// warnLinkPreviewThumbnail prints an applyLinkPreviewImage warning, sanitized
// because it carries upload error detail.
func warnLinkPreviewThumbnail(w io.Writer, msg string) {
	if msg == "" {
		return
	}
	fmt.Fprintf(w, "warning: %s\n", out.SanitizeHuman(msg))
}
