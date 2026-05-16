package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const maxSendFileSize = 100 * 1024 * 1024
const imageThumbnailMaxDimension = 96
const voiceWaveformSamples = 64
const voiceWaveformMax = 100

type sendFileOptions struct {
	filename      string
	caption       string
	mimeOverride  string
	replyTo       string
	replyToSender string
	ptt           bool
}

type voiceNoteMetadata struct {
	seconds  uint32
	waveform []byte
}

func sendFile(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, to types.JID, filePath string, opts sendFileOptions) (string, map[string]string, error) {
	data, err := readSendFileData(filePath)
	if err != nil {
		return "", nil, err
	}

	name := strings.TrimSpace(opts.filename)
	if name == "" {
		name = filepath.Base(filePath)
	}
	mimeType := detectSendFileMIME(filePath, opts.mimeOverride, data)
	if opts.ptt && !isOggOpusMIME(mimeType) {
		return "", nil, fmt.Errorf("voice notes require OGG Opus audio; got %s", mimeType)
	}

	mediaType := "document"
	uploadType, _ := wa.MediaTypeFromString("document")
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = "image"
		uploadType, _ = wa.MediaTypeFromString("image")
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = "video"
		uploadType, _ = wa.MediaTypeFromString("video")
	case strings.HasPrefix(mimeType, "audio/"):
		mediaType = "audio"
		uploadType, _ = wa.MediaTypeFromString("audio")
	}

	isNewsletter := to.Server == types.NewsletterServer
	if isNewsletter && opts.ptt {
		return "", nil, fmt.Errorf("voice-note mode is not supported for channels; omit --ptt to send audio")
	}
	if isNewsletter && (strings.TrimSpace(opts.replyTo) != "" || strings.TrimSpace(opts.replyToSender) != "") {
		return "", nil, fmt.Errorf("quoted file replies are not supported for channels")
	}

	var up whatsmeow.UploadResponse
	if isNewsletter {
		up, err = a.WA().UploadNewsletter(ctx, data, uploadType)
	} else {
		up, err = a.WA().Upload(ctx, data, uploadType)
	}
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	msg := &waProto.Message{}
	var replyContext *waProto.ContextInfo
	if !isNewsletter {
		replyContext, err = buildReplyContextInfo(a.DB(), to, opts.replyTo, opts.replyToSender)
		if err != nil {
			return "", nil, err
		}
	}
	voiceMeta := voiceNoteMetadata{}
	if opts.ptt {
		voiceMeta = loadVoiceNoteMetadata(ctx, filePath)
	}

	switch mediaType {
	case "image":
		imageMsg, err := newImageMessage(up, mimeType, opts.caption, data)
		if err != nil {
			return "", nil, err
		}
		msg.ImageMessage = imageMsg
	case "video":
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(opts.caption),
		}
	case "audio":
		msg.AudioMessage = newAudioMessage(up, mimeType, opts.ptt, voiceMeta)
	default:
		msg.DocumentMessage = &waProto.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			FileName:      proto.String(name),
			Caption:       proto.String(opts.caption),
			Title:         proto.String(name),
		}
	}
	attachSendFileReplyContext(msg, replyContext)

	var id types.MessageID
	if isNewsletter {
		id, err = a.WA().SendProtoMessageWithExtra(ctx, to, msg, up.Handle)
	} else {
		id, err = a.WA().SendProtoMessage(ctx, to, msg)
	}
	if err != nil {
		return "", nil, err
	}

	if to == types.StatusBroadcastJID {
		_ = a.DB().UpsertStatusMessage(store.UpsertStatusMessageParams{
			MsgID:         id,
			Timestamp:     now,
			FromMe:        true,
			SenderName:    "me",
			Text:          opts.caption,
			MediaType:     mediaType,
			MediaCaption:  opts.caption,
			Filename:      name,
			MimeType:      mimeType,
			DirectPath:    up.DirectPath,
			MediaKey:      up.MediaKey,
			FileSHA256:    up.FileSHA256,
			FileEncSHA256: up.FileEncSHA256,
			FileLength:    up.FileLength,
		})
	} else {
		chatName := a.WA().ResolveChatName(ctx, to, "")
		kind := chatKindFromJID(to)
		_ = a.DB().UpsertChat(to.String(), kind, chatName, now)
		_ = a.DB().UpsertMessage(store.UpsertMessageParams{
			ChatJID:       to.String(),
			ChatName:      chatName,
			MsgID:         id,
			SenderJID:     "",
			SenderName:    "me",
			Timestamp:     now,
			FromMe:        true,
			Text:          opts.caption,
			MediaType:     mediaType,
			MediaCaption:  opts.caption,
			Filename:      name,
			MimeType:      mimeType,
			DirectPath:    up.DirectPath,
			MediaKey:      up.MediaKey,
			FileSHA256:    up.FileSHA256,
			FileEncSHA256: up.FileEncSHA256,
			FileLength:    up.FileLength,
		})
	}

	return id, map[string]string{
		"name":      name,
		"mime_type": mimeType,
		"media":     mediaType,
		"ptt":       strconv.FormatBool(opts.ptt),
	}, nil
}

func newImageMessage(up whatsmeow.UploadResponse, mimeType, caption string, data []byte) (*waProto.ImageMessage, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invalid image data: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("invalid image dimensions: %dx%d", cfg.Width, cfg.Height)
	}

	msg := &waProto.ImageMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		Mimetype:      proto.String(mimeType),
		Caption:       proto.String(caption),
		Height:        proto.Uint32(uint32(cfg.Height)),
		Width:         proto.Uint32(uint32(cfg.Width)),
	}
	if thumbnail, err := imageJPEGThumbnail(data); err == nil && len(thumbnail) > 0 {
		msg.JPEGThumbnail = thumbnail
	}
	return msg, nil
}

func imageJPEGThumbnail(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, fmt.Errorf("invalid image dimensions: %dx%d", srcW, srcH)
	}

	dstW, dstH := scaledDimensions(srcW, srcH, imageThumbnailMaxDimension)
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			srcX := bounds.Min.X + x*srcW/dstW
			srcY := bounds.Min.Y + y*srcH/dstH
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 75}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func scaledDimensions(width, height, maxDimension int) (int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	if maxDimension <= 0 || (width <= maxDimension && height <= maxDimension) {
		return width, height
	}
	if width >= height {
		scaledHeight := height * maxDimension / width
		if scaledHeight < 1 {
			scaledHeight = 1
		}
		return maxDimension, scaledHeight
	}
	scaledWidth := width * maxDimension / height
	if scaledWidth < 1 {
		scaledWidth = 1
	}
	return scaledWidth, maxDimension
}

func newAudioMessage(up whatsmeow.UploadResponse, mimeType string, ptt bool, meta voiceNoteMetadata) *waProto.AudioMessage {
	msg := &waProto.AudioMessage{
		URL:           proto.String(up.URL),
		DirectPath:    proto.String(up.DirectPath),
		MediaKey:      up.MediaKey,
		FileEncSHA256: up.FileEncSHA256,
		FileSHA256:    up.FileSHA256,
		FileLength:    proto.Uint64(up.FileLength),
		Mimetype:      proto.String(mimeType),
		PTT:           proto.Bool(ptt),
	}
	if ptt {
		if meta.seconds > 0 {
			msg.Seconds = proto.Uint32(meta.seconds)
		}
		if len(meta.waveform) == voiceWaveformSamples {
			msg.Waveform = meta.waveform
		}
	}
	return msg
}

func readSendFileData(filePath string) ([]byte, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSendFileSize {
		return nil, fmt.Errorf("file too large (%d bytes); maximum send file size is %d bytes", info.Size(), maxSendFileSize)
	}
	return os.ReadFile(filePath)
}

func attachSendFileReplyContext(msg *waProto.Message, info *waProto.ContextInfo) {
	if info == nil {
		return
	}
	switch {
	case msg.GetImageMessage() != nil:
		msg.ImageMessage.ContextInfo = info
	case msg.GetVideoMessage() != nil:
		msg.VideoMessage.ContextInfo = info
	case msg.GetAudioMessage() != nil:
		msg.AudioMessage.ContextInfo = info
	case msg.GetDocumentMessage() != nil:
		msg.DocumentMessage.ContextInfo = info
	}
}

func chatKindFromJID(j types.JID) string {
	if j.Server == types.NewsletterServer {
		return "newsletter"
	}
	if j.Server == types.GroupServer {
		return "group"
	}
	if j.IsBroadcastList() {
		return "broadcast"
	}
	if j.Server == types.DefaultUserServer {
		return "dm"
	}
	return "unknown"
}

func detectSendFileMIME(filePath, mimeOverride string, data []byte) string {
	mimeType := strings.TrimSpace(mimeOverride)
	if mimeType == "" {
		// Use filePath for MIME detection, not the display name override.
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}
	if mimeType == "audio/ogg" || mimeType == "application/ogg" {
		return "audio/ogg; codecs=opus"
	}
	return mimeType
}

func isOggOpusMIME(mimeType string) bool {
	mediaType, params, err := mime.ParseMediaType(mimeType)
	if err != nil {
		return false
	}
	codecs := strings.ToLower(params["codecs"])
	return mediaType == "audio/ogg" && strings.Contains(codecs, "opus")
}

func loadVoiceNoteMetadata(ctx context.Context, filePath string) voiceNoteMetadata {
	return voiceNoteMetadata{
		seconds:  probeAudioSeconds(ctx, filePath),
		waveform: probeAudioWaveform(ctx, filePath),
	}
}

func probeAudioSeconds(ctx context.Context, filePath string) uint32 {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(probeCtx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	).Output()
	if err != nil {
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return uint32(math.Ceil(seconds))
}

func probeAudioWaveform(ctx context.Context, filePath string) []byte {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(probeCtx, "ffmpeg",
		"-v", "error",
		"-i", filePath,
		"-ac", "1",
		"-ar", "8000",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	).Output()
	if err != nil {
		return nil
	}
	return waveformFromPCM16LE(out)
}

func waveformFromPCM16LE(data []byte) []byte {
	waveform := make([]byte, voiceWaveformSamples)
	sampleCount := len(data) / 2
	if sampleCount == 0 {
		return waveform
	}

	bucketSize := int(math.Ceil(float64(sampleCount) / voiceWaveformSamples))
	levels := make([]float64, voiceWaveformSamples)
	var maxLevel float64
	for i := 0; i < voiceWaveformSamples; i++ {
		start := i * bucketSize
		if start >= sampleCount {
			break
		}
		end := start + bucketSize
		if end > sampleCount {
			end = sampleCount
		}

		var sum float64
		for sampleIndex := start; sampleIndex < end; sampleIndex++ {
			offset := sampleIndex * 2
			sample := int16(binary.LittleEndian.Uint16(data[offset : offset+2]))
			sum += math.Abs(float64(sample))
		}
		levels[i] = sum / float64(end-start)
		if levels[i] > maxLevel {
			maxLevel = levels[i]
		}
	}
	if maxLevel == 0 {
		return waveform
	}

	for i, level := range levels {
		normalized := math.Round((level / maxLevel) * voiceWaveformMax)
		if normalized > voiceWaveformMax {
			normalized = voiceWaveformMax
		}
		waveform[i] = byte(normalized)
	}
	return waveform
}
