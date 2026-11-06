package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/linkpreview"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type failingUploadTextSender struct {
	recordingTextSender
}

func (s *failingUploadTextSender) Upload(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, errors.New("upload \x1b[31mrefused\x1b[0m")
}

// Delegated text sends reach the HQ upload inside `sync --follow`, whose
// stderr may be an NDJSON event stream: the upload failure must come back to
// the caller instead of being printed by the send path.
func TestSendTextHQUploadFailureIsReturnedNotPrinted(t *testing.T) {
	to := types.NewJID("15551234567", types.DefaultUserServer)
	sender := &failingUploadTextSender{recordingTextSender{linkedJID: "15550000000@s.whatsapp.net"}}
	preview := &linkpreview.Preview{URL: "https://example.com/", Title: "Example", Thumbnail: testPNG(t, 800, 400)}

	var warnings []string
	stderr := captureRootStderr(t, func() {
		_, err := sendTextMessageWithSender(context.Background(), sender, openSendTestDB(t), to, "see https://example.com/", "", "", preview, nil, textEphemeralOptions{}, textSendOptions{
			warn: func(msg string) { warnings = append(warnings, msg) },
		})
		if err != nil {
			t.Errorf("sendTextMessageWithSender: %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("send path wrote to stderr: %q", stderr)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "HQ thumbnail upload failed") {
		t.Fatalf("warnings = %q", warnings)
	}
	if sender.protoCalls != 1 || len(sender.protoMsg.GetExtendedTextMessage().GetJPEGThumbnail()) == 0 {
		t.Fatalf("message not sent with the small preview fallback")
	}
}

func TestDelegatedThumbWarningRendersInCaller(t *testing.T) {
	raw, err := json.Marshal(sendDelegateResponse{OK: true, Sent: true, To: "chat", ID: "id1", ThumbWarning: "link preview HQ thumbnail upload failed, using small preview: \x1b[31mboom\x1b[0m"})
	if err != nil {
		t.Fatal(err)
	}
	var resp sendDelegateResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ThumbWarning == "" {
		t.Fatalf("thumbnail_warning lost across the delegate socket: %s", raw)
	}

	var stdout string
	stderr := captureRootStderr(t, func() {
		stdout = captureRootStdout(t, func() {
			if err := writeDelegatedSendOutput(&rootFlags{asJSON: true}, "text", resp); err != nil {
				t.Errorf("writeDelegatedSendOutput: %v", err)
			}
		})
	})
	if strings.Contains(stderr, "\x1b") || !strings.Contains(stderr, "warning: link preview HQ thumbnail upload failed") || !strings.Contains(stderr, "boom") {
		t.Fatalf("caller stderr = %q", stderr)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(stdout), &body); err != nil {
		t.Fatalf("stdout is not JSON: %q", stdout)
	}
}
