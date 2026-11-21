package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"math"
	"mime"
	"net/http"
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

// maxSendFileSize is the default upload cap. The whole file is read into memory
// before upload, so it stays conservative; `send file --max-size-mb` raises it
// per send up to maxSendFileSizeLimit, WhatsApp's own limit for documents.
const maxSendFileSize = 100 * 1024 * 1024
const maxSendFileSizeLimit int64 = 2048 * 1024 * 1024
const imageThumbnailMaxDimension = 96
const imageThumbnailMaxPixels = 40_000_000
const voiceWaveformSamples = 64
const voiceWaveformMax = 100
const voiceWaveformSampleRate = 8000
const maxWaveformPCMBytes = 2 << 20 // 2 MiB of 8 kHz s16le is ~131s of PCM

const sendMediaTypeAuto = "auto"

type sendFileOptions struct {
	filename      string
	caption       string
	mimeOverride  string
	mediaAs       string
	replyTo       string
	replyToSender string
	ptt           bool
	// maxSize is the upload cap in bytes; 0 means maxSendFileSize.
	maxSize int64
	// voice is a voice note's length and waveform as the caller measured
	// them. A part left zero is probed from the file with ffprobe or ffmpeg.
	voice voiceNoteMetadata
}

type voiceNoteMetadata struct {
	seconds  uint32
	waveform []byte
}

// parseVoiceNoteMetadata checks a voice note's length and waveform as a caller
// gives them (the send request's seconds and waveform, or --seconds and
// --waveform). Either may be absent (nil) and is then probed from the file at
// send time; one that is present and malformed is refused, naming its field.
func parseVoiceNoteMetadata(seconds *int64, waveform []int, ptt bool) (voiceNoteMetadata, error) {
	if seconds == nil && waveform == nil {
		return voiceNoteMetadata{}, nil
	}
	if !ptt {
		var given []string
		if seconds != nil {
			given = append(given, "seconds")
		}
		if waveform != nil {
			given = append(given, "waveform")
		}
		return voiceNoteMetadata{}, fmt.Errorf("%s only apply to a voice note (send voice, --ptt, or kind voice)", strings.Join(given, " and "))
	}
	var meta voiceNoteMetadata
	if seconds != nil {
		if *seconds < 1 || *seconds > math.MaxUint32 {
			return voiceNoteMetadata{}, fmt.Errorf("seconds must be a whole number from 1 to %d, got %d", uint32(math.MaxUint32), *seconds)
		}
		meta.seconds = uint32(*seconds)
	}
	if waveform != nil {
		if len(waveform) != voiceWaveformSamples {
			return voiceNoteMetadata{}, fmt.Errorf("waveform must have exactly %d levels, got %d", voiceWaveformSamples, len(waveform))
		}
		meta.waveform = make([]byte, voiceWaveformSamples)
		for i, level := range waveform {
			if level < 0 || level > voiceWaveformMax {
				return voiceNoteMetadata{}, fmt.Errorf("waveform[%d] is %d; each level must be 0..%d", i, level, voiceWaveformMax)
			}
			meta.waveform[i] = byte(level)
		}
	}
	return meta, nil
}

// sendFileOutcome is the result of a completed file send. storeWarning is
// non-nil when WhatsApp accepted the message but recording it in local
// history failed; callers must surface the warning without discarding the
// delivered ID, since reporting a hard error would invite automation to
// retry an already-delivered message. voiceWarning says which of a voice
// note's length and waveform could not be measured and so was left out.
type sendFileOutcome struct {
	id           string
	meta         map[string]string
	storeWarning error
	voiceWarning string
}

func sendFile(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, to types.JID, filePath string, opts sendFileOptions) (sendFileOutcome, error) {
	mediaAs, err := validateSendFileMediaOptions(opts.mediaAs, opts.ptt)
	if err != nil {
		return sendFileOutcome{}, err
	}
	opts.mediaAs = mediaAs
	if !opts.ptt && (opts.voice.seconds != 0 || opts.voice.waveform != nil) {
		return sendFileOutcome{}, fmt.Errorf("a voice note's seconds and waveform need voice-note mode")
	}

	maxSize, err := resolveSendFileMaxSize(opts.maxSize)
	if err != nil {
		return sendFileOutcome{}, err
	}
	data, err := readSendFileDataLimited(filePath, maxSize)
	if err != nil {
		return sendFileOutcome{}, err
	}

	name := strings.TrimSpace(opts.filename)
	if name == "" {
		name = filepath.Base(filePath)
	}
	mimeType := detectSendFileMIME(filePath, opts.mimeOverride, data)
	if opts.ptt && !isOggOpusMIME(mimeType) {
		return sendFileOutcome{}, fmt.Errorf("voice notes require OGG Opus audio; got %s", mimeType)
	}

	mediaType, uploadType, err := resolveSendMediaType(mimeType, opts.mediaAs)
	if err != nil {
		return sendFileOutcome{}, err
	}

	isNewsletter := to.Server == types.NewsletterServer
	if isNewsletter && opts.ptt {
		return sendFileOutcome{}, fmt.Errorf("voice-note mode is not supported for channels; omit --ptt to send audio")
	}
	if isNewsletter && (strings.TrimSpace(opts.replyTo) != "" || strings.TrimSpace(opts.replyToSender) != "") {
		return sendFileOutcome{}, fmt.Errorf("quoted file replies are not supported for channels")
	}
	// Quote before uploading, so a message that cannot be quoted is refused
	// without spending an upload.
	var replyContext *waProto.ContextInfo
	if !isNewsletter {
		replyContext, err = buildReplyContextInfo(ctx, a.WA(), a.DB(), to, opts.replyTo, opts.replyToSender)
		if err != nil {
			return sendFileOutcome{}, err
		}
	}

	var up whatsmeow.UploadResponse
	if isNewsletter {
		up, err = a.WA().UploadNewsletter(ctx, data, uploadType)
	} else {
		up, err = a.WA().Upload(ctx, data, uploadType)
	}
	if err != nil {
		return sendFileOutcome{}, err
	}

	now := time.Now().UTC()
	msg := &waProto.Message{}
	voiceMeta := voiceNoteMetadata{}
	var voiceWarning string
	if opts.ptt {
		voiceMeta, voiceWarning = completeVoiceNoteMetadata(ctx, filePath, opts.voice)
	}

	switch mediaType {
	case "image":
		imageMsg, err := newImageMessage(up, mimeType, opts.caption, data)
		if err != nil {
			return sendFileOutcome{}, err
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
		return sendFileOutcome{}, err
	}

	// The send already succeeded; store failures below surface as a warning
	// on the outcome instead of an error so history divergence is visible
	// without turning a delivered message into a reported failure.
	var storeErr error
	if to == types.StatusBroadcastJID {
		storeErr = a.DB().UpsertStatusMessage(store.UpsertStatusMessageParams{
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
		// Match text-send and receipt history using the authoritative LID
		// mapping. Keep the actual wire destination and accepted ID unchanged.
		storedChat := to.ToNonAD()
		if to.Server == types.HiddenUserServer {
			storedChat = canonicalOutboundChat(ctx, a.WA(), to)
		}
		chatName := a.WA().ResolveChatName(ctx, storedChat, "")
		kind := chatKindFromJID(storedChat)
		if err := a.DB().UpsertChat(storedChat.String(), kind, chatName, now); err != nil {
			storeErr = fmt.Errorf("chat update: %w", err)
		}
		if err := a.DB().UpsertMessage(store.UpsertMessageParams{
			ChatJID:         storedChat.String(),
			ChatName:        chatName,
			MsgID:           id,
			SenderJID:       "",
			SenderName:      "me",
			Timestamp:       now,
			FromMe:          true,
			Text:            opts.caption,
			QuotedMsgID:     replyContext.GetStanzaID(),
			QuotedSenderJID: canonicalOutboundQuoteSender(ctx, a.WA(), replyContext.GetParticipant()),
			MediaType:       mediaType,
			MediaCaption:    opts.caption,
			Filename:        name,
			MimeType:        mimeType,
			DirectPath:      up.DirectPath,
			MediaKey:        up.MediaKey,
			FileSHA256:      up.FileSHA256,
			FileEncSHA256:   up.FileEncSHA256,
			FileLength:      up.FileLength,
		}); err != nil {
			storeErr = errors.Join(storeErr, fmt.Errorf("message update: %w", err))
		}
	}

	return sendFileOutcome{
		id: id,
		meta: map[string]string{
			"name":      name,
			"mime_type": mimeType,
			"media":     mediaType,
			"ptt":       strconv.FormatBool(opts.ptt),
		},
		storeWarning: storeErr,
		voiceWarning: voiceWarning,
	}, nil
}

// warnSendStoreFailure reports a post-delivery local-history failure on the
// human channel. Kept alongside sendFileOutcome so every caller phrases the
// partial success the same way.
func warnSendStoreFailure(w io.Writer, id string, storeWarning error) {
	if storeWarning == nil {
		return
	}
	warnSendStoreFailureMsg(w, id, storeWarning.Error())
}

// warnSendStoreFailureMsg is the string form for callers that receive the
// warning across the IPC boundary.
func warnSendStoreFailureMsg(w io.Writer, id, msg string) {
	if msg == "" {
		return
	}
	fmt.Fprintf(w, "warning: message delivered (id %s) but local history update failed: %s\n", id, msg)
}

// addStoreWarning annotates a JSON success payload with the partial-success
// marker; absent key means local history was written.
func addStoreWarning(payload map[string]any, storeWarning error) map[string]any {
	if storeWarning != nil {
		payload["store_warning"] = storeWarning.Error()
	}
	return payload
}

// resolveSendMediaType decides the WhatsApp message type (and matching upload
// type) for an outbound file. By default it derives the type from the MIME
// prefix. A non-empty override other than "auto" (from --as) forces the type,
// so a caller can, e.g., send an mp3 as a downloadable document instead of an
// inline audio bubble — WhatsApp derives the bubble from the message type, not
// the MIME. Only document/audio/image/video may be forced.
func resolveSendMediaType(mimeType, override string) (string, whatsmeow.MediaType, error) {
	as, err := normalizeSendMediaTypeOverride(override)
	if err != nil {
		return "", "", err
	}
	if as != sendMediaTypeAuto {
		ut, _ := wa.MediaTypeFromString(as)
		return as, ut, nil
	}
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		ut, _ := wa.MediaTypeFromString("image")
		return "image", ut, nil
	case strings.HasPrefix(mimeType, "video/"):
		ut, _ := wa.MediaTypeFromString("video")
		return "video", ut, nil
	case strings.HasPrefix(mimeType, "audio/"):
		ut, _ := wa.MediaTypeFromString("audio")
		return "audio", ut, nil
	}
	ut, _ := wa.MediaTypeFromString("document")
	return "document", ut, nil
}

func validateSendFileMediaOptions(override string, ptt bool) (string, error) {
	as, err := normalizeSendMediaTypeOverride(override)
	if err != nil {
		return "", err
	}
	if ptt && as != sendMediaTypeAuto && as != "audio" {
		return "", fmt.Errorf("--ptt may only be used with --as auto or --as audio")
	}
	return as, nil
}

func normalizeSendMediaTypeOverride(override string) (string, error) {
	switch as := strings.ToLower(strings.TrimSpace(override)); as {
	case "", sendMediaTypeAuto:
		return sendMediaTypeAuto, nil
	case "document", "audio", "image", "video":
		return as, nil
	default:
		return "", fmt.Errorf("invalid --as %q (want auto|document|audio|image|video)", override)
	}
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
	if cfg.Width <= imageThumbnailMaxPixels/cfg.Height {
		if thumbnail, err := imageJPEGThumbnail(data); err == nil && len(thumbnail) > 0 {
			msg.JPEGThumbnail = thumbnail
		}
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
	return readSendFileDataLimited(filePath, maxSendFileSize)
}

func readSendFileDataLimited(filePath string, maxBytes int64) ([]byte, error) {
	return readRegularFileLimitedWithOpener(filePath, maxBytes, openOutboundMedia)
}

// resolveSendFileMaxSize turns a requested cap (0 = default) into bytes, refusing
// values WhatsApp would reject anyway.
func resolveSendFileMaxSize(requested int64) (int64, error) {
	switch {
	case requested == 0:
		return maxSendFileSize, nil
	case requested < 0:
		return 0, fmt.Errorf("max file size must be positive")
	case requested > maxSendFileSizeLimit:
		return 0, fmt.Errorf("max file size %d bytes exceeds WhatsApp's %d byte limit", requested, int64(maxSendFileSizeLimit))
	}
	return requested, nil
}

// sendFileMaxSizeFromMB converts the --max-size-mb flag (MiB) into bytes.
func sendFileMaxSizeFromMB(mb int64) (int64, error) {
	if mb <= 0 {
		return 0, fmt.Errorf("--max-size-mb must be positive")
	}
	// Bound the input before multiplication so huge values cannot wrap.
	if mb > maxSendFileSizeLimit/(1024*1024) {
		return 0, fmt.Errorf("--max-size-mb must not exceed 2048")
	}
	return mb * 1024 * 1024, nil
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
		ext := strings.ToLower(filepath.Ext(filePath))
		if ext == ".m4a" {
			mimeType = "audio/mp4"
		} else {
			mimeType = mime.TypeByExtension(ext)
		}
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

// completeVoiceNoteMetadata fills in what the caller left out of a voice note's
// length and waveform from the file itself, with ffprobe and ffmpeg. A part the
// caller gave is used as given and never probed. A probe that fails leaves its
// part out of the message, which WhatsApp accepts, and the returned warning
// says why.
func completeVoiceNoteMetadata(ctx context.Context, filePath string, given voiceNoteMetadata) (voiceNoteMetadata, string) {
	meta := given
	var warnings []string
	if meta.seconds == 0 {
		seconds, err := probeAudioSeconds(ctx, filePath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("voice note length not measured: %v", err))
		}
		meta.seconds = seconds
	}
	if meta.waveform == nil {
		waveform, err := probeAudioWaveform(ctx, filePath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("voice note waveform not measured: %v", err))
		}
		meta.waveform = waveform
	}
	return meta, strings.Join(warnings, "; ")
}

// probeError names the probe that failed, says plainly when it is not
// installed, as in an image without ffmpeg, and adds the first line it wrote
// to stderr.
func probeError(tool string, err error, stderr *probeStderr) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s is not installed", tool)
	}
	if detail, _, _ := strings.Cut(strings.TrimSpace(string(stderr.head)), "\n"); detail != "" {
		return fmt.Errorf("%s: %w: %s", tool, err, detail)
	}
	return fmt.Errorf("%s: %w", tool, err)
}

// probeStderr keeps the start of what a probe writes to stderr, enough for
// its first error line, and drops the rest.
type probeStderr struct{ head []byte }

func (w *probeStderr) Write(p []byte) (int, error) {
	if room := 512 - len(w.head); room > 0 {
		w.head = append(w.head, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func probeAudioSeconds(ctx context.Context, filePath string) (uint32, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	var stderr probeStderr
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return 0, probeError("ffprobe", err, &stderr)
	}
	text := strings.TrimSpace(string(out))
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("ffprobe gave no positive duration (%q)", text)
	}
	return uint32(math.Ceil(seconds)), nil
}

func probeAudioWaveform(ctx context.Context, filePath string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	probeSeconds := float64(maxWaveformPCMBytes) / float64(voiceWaveformSampleRate*2)
	cmd := exec.CommandContext(probeCtx, "ffmpeg",
		"-v", "error",
		"-i", filePath,
		"-t", strconv.FormatFloat(probeSeconds, 'f', 3, 64),
		"-ac", "1",
		"-ar", strconv.Itoa(voiceWaveformSampleRate),
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-fs", strconv.Itoa(maxWaveformPCMBytes),
		"-",
	)
	var stderr probeStderr
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, probeError("ffmpeg", err, &stderr)
	}
	if err := cmd.Start(); err != nil {
		return nil, probeError("ffmpeg", err, &stderr)
	}
	out, err := io.ReadAll(io.LimitReader(stdout, int64(maxWaveformPCMBytes)))
	reachedLimit := len(out) == maxWaveformPCMBytes
	if reachedLimit {
		cancel()
	}
	waitErr := cmd.Wait()
	// Killing the decoder at the byte limit is expected; other failures must
	// not turn a partial decode into a valid waveform.
	switch {
	case err != nil:
		return nil, probeError("ffmpeg", err, &stderr)
	case !reachedLimit && waitErr != nil:
		return nil, probeError("ffmpeg", waitErr, &stderr)
	case len(out) == 0:
		return nil, fmt.Errorf("ffmpeg decoded no audio")
	}
	return waveformFromPCM16LE(out), nil
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
