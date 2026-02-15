package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func sendFile(ctx context.Context, a interface {
	WA() app.WAClient
	DB() *store.DB
}, to types.JID, filePath, filename, caption, mimeOverride string, ptt bool) (string, map[string]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", nil, err
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		name = filepath.Base(filePath)
	}
	mimeType := strings.TrimSpace(mimeOverride)
	if mimeType == "" {
		// Use filePath for MIME detection, not the display name override
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}

	// WhatsApp requires "audio/ogg; codecs=opus" for OGG audio to be
	// delivered correctly. Go's mime detection returns "audio/ogg" which
	// causes silent delivery failure. Fix the MIME for any OGG audio.
	if mimeType == "audio/ogg" || mimeType == "application/ogg" {
		mimeType = "audio/ogg; codecs=opus"
	}

	// When --ptt is set, force audio MIME if not already audio.
	if ptt && !strings.HasPrefix(mimeType, "audio/") {
		mimeType = "audio/ogg; codecs=opus"
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

	up, err := a.WA().Upload(ctx, data, uploadType)
	if err != nil {
		return "", nil, err
	}

	now := time.Now().UTC()
	msg := &waProto.Message{}

	switch mediaType {
	case "image":
		msg.ImageMessage = &waProto.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "video":
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "audio":
		audioMsg := &waProto.AudioMessage{
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
			if dur, err := probeAudioDuration(filePath); err == nil && dur > 0 {
				audioMsg.Seconds = proto.Uint32(dur)
			}
			audioMsg.Waveform = generateWaveform(data)
		}
		msg.AudioMessage = audioMsg
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
			Caption:       proto.String(caption),
			Title:         proto.String(name),
		}
	}

	id, err := a.WA().SendProtoMessage(ctx, to, msg)
	if err != nil {
		return "", nil, err
	}

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
		Text:          caption,
		MediaType:     mediaType,
		MediaCaption:  caption,
		Filename:      name,
		MimeType:      mimeType,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return id, map[string]string{
		"name":      name,
		"mime_type": mimeType,
		"media":     mediaType,
	}, nil
}

// probeAudioDuration uses ffprobe to get the duration of an audio file in seconds.
// Returns 0 if ffprobe is not available or fails.
func probeAudioDuration(path string) (uint32, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	s := strings.TrimSpace(string(out))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", s, err)
	}
	return uint32(math.Ceil(f)), nil
}

// generateWaveform produces a 64-sample waveform (values 0-100) from raw audio data.
// This is a simplified RMS-based approach that samples evenly across the file.
// For OGG Opus files this operates on the raw bytes which gives a reasonable
// approximation of the audio energy distribution.
func generateWaveform(data []byte) []byte {
	const numSamples = 64
	if len(data) == 0 {
		return make([]byte, numSamples)
	}

	waveform := make([]byte, numSamples)
	chunkSize := len(data) / numSamples
	if chunkSize < 2 {
		chunkSize = 2
	}

	var maxRMS float64
	rmsValues := make([]float64, numSamples)

	for i := 0; i < numSamples; i++ {
		start := i * (len(data) / numSamples)
		end := start + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[start:end]

		// Compute RMS of 16-bit little-endian samples from the raw bytes.
		var sumSq float64
		var count int
		for j := 0; j+1 < len(chunk); j += 2 {
			sample := int16(binary.LittleEndian.Uint16(chunk[j : j+2]))
			sumSq += float64(sample) * float64(sample)
			count++
		}
		if count > 0 {
			rmsValues[i] = math.Sqrt(sumSq / float64(count))
			if rmsValues[i] > maxRMS {
				maxRMS = rmsValues[i]
			}
		}
	}

	// Normalize to 0-100 range.
	if maxRMS > 0 {
		for i, v := range rmsValues {
			normalized := (v / maxRMS) * 100
			if normalized > 100 {
				normalized = 100
			}
			waveform[i] = byte(normalized)
		}
	}

	return waveform
}

func chatKindFromJID(j types.JID) string {
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
