package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

func runStatusCLI(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	if exe := os.Getenv("WACLI_PROOF_CLI"); exe != "" {
		cmd := exec.Command(exe, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	return runPresenceDelegateHelper(t, args)
}

func TestStatusDelegatesWhileStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, media := range []bool{false, true} {
		name := "text"
		if media {
			name = "media"
		}
		t.Run(name, func(t *testing.T) {
			dir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			got := make(chan sendDelegateRequest, 1)
			stop, err := startSendDelegateServerForStore(context.Background(), dir, sendSpacing{}, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				got <- req
				resp := sendDelegateResponse{OK: true, Sent: true, To: "status@broadcast", ID: "status-proof"}
				if media {
					resp.File = map[string]string{"media": "image", "mime_type": "image/png"}
				}
				return resp, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := []string{"--store", dir, "--json", "--timeout", "2s", "send", "status", "--message", "synthetic status", "--post-send-wait", "0"}
			file := ""
			if media {
				file = filepath.Join(t.TempDir(), "status.png")
				if err := os.WriteFile(file, []byte("synthetic image"), 0600); err != nil {
					t.Fatal(err)
				}
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(cwd, file)
				if err != nil {
					t.Fatal(err)
				}
				args = append(args, "--file", rel, "--mime", "image/png")
			} else {
				args = append(args, "--background-color", "#ff00aa", "--font", "2")
			}
			stdout, stderr, err := runStatusCLI(t, args)
			if err != nil {
				t.Fatalf("status failed: %v stdout=%s stderr=%s", err, stdout, stderr)
			}
			req := <-got
			if req.Kind != "status" || req.Message != "synthetic status" {
				t.Fatalf("request: %+v", req)
			}
			if !media && (req.StatusBackground != "#ff00aa" || req.StatusFont == nil || *req.StatusFont != 2) {
				t.Fatalf("style request: %+v", req)
			}
			if media && (req.File != file || req.MIME != "image/png") {
				t.Fatalf("media request: %+v", req)
			}
			if !strings.Contains(stdout, `"id":"status-proof"`) || !strings.Contains(stdout, `"to":"status@broadcast"`) {
				t.Fatalf("output: %s", stdout)
			}
			if media && (!strings.Contains(stdout, `"media":"image"`) || strings.Contains(stdout, `"file":`)) {
				t.Fatalf("media output shape: %s", stdout)
			}
			t.Logf("built CLI %s status: %s", name, stdout)
		})
	}
}

func TestStatusDelegateKeepsOriginalDeadline(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	dir := shortPresenceDelegateStoreDir(t)
	got := make(chan sendDelegateRequest, 1)
	stop, err := startSendDelegateServerForStore(context.Background(), dir, sendSpacing{}, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		got <- req
		return sendDelegateResponse{OK: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_, err = delegateSend(ctx, &rootFlags{storeDir: dir, timeout: 5 * time.Second}, sendDelegateRequest{Kind: "status", Message: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if req := <-got; req.DeadlineUnixMS > deadline.UnixMilli() {
		t.Fatalf("deadline extended: %d > %d", req.DeadlineUnixMS, deadline.UnixMilli())
	}
}

type statusWireWA struct {
	*fakeSendFileWA
	message *waProto.Message
}

func (f *statusWireWA) SendProtoMessage(_ context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	f.wireTarget, f.message = to, msg
	return f.sentID, nil
}

func TestSendStatusPreservesWireAndHistory(t *testing.T) {
	for _, media := range []bool{false, true} {
		for _, failStore := range []bool{false, true} {
			name := fmt.Sprintf("media=%t/failStore=%t", media, failStore)
			t.Run(name, func(t *testing.T) {
				a, db, file := newSendFileFixture(t)
				wire := &statusWireWA{fakeSendFileWA: a.wa.(*fakeSendFileWA)}
				a.wa = wire
				if failStore {
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
				font := int32(2)
				req := sendDelegateRequest{Kind: sendStatusKind, Message: "synthetic status", StatusBackground: "#ff00aa", StatusFont: &font}
				if media {
					req.File, req.MIME = file, "text/plain"
				}
				resp, err := sendStatus(context.Background(), a, req)
				if err != nil || !resp.Sent || resp.ID != "MSG1" || resp.To != "status@broadcast" || (resp.StoreWarning != "") != failStore {
					t.Fatalf("response=%+v err=%v", resp, err)
				}
				if wire.wireTarget != types.StatusBroadcastJID {
					t.Fatalf("wire target=%s", wire.wireTarget)
				}
				if !media {
					ext := wire.message.GetExtendedTextMessage()
					if ext.GetText() != req.Message || ext.GetBackgroundArgb() != 0xffff00aa || ext.GetFont() != waProto.ExtendedTextMessage_FontType(2) {
						t.Fatalf("wire text=%v", ext)
					}
				} else if resp.File["mime_type"] != "text/plain" || wire.message.GetDocumentMessage().GetCaption() != req.Message {
					t.Fatalf("wire media=%v response=%+v", wire.message, resp)
				}
				if !failStore {
					saved, err := db.GetStatusMessage(resp.ID)
					if err != nil || !saved.FromMe {
						t.Fatalf("stored=%+v err=%v", saved, err)
					}
					if !media && (saved.Text != req.Message || saved.BackgroundColor != req.StatusBackground || saved.Font != font) {
						t.Fatalf("stored style=%+v", saved)
					}
				}
			})
		}
	}
}

func TestStatusRejectsInvalidDelegateBeforeAccessingApp(t *testing.T) {
	for _, req := range []sendDelegateRequest{
		{Message: "   "},
		{Message: "status", StatusBackground: "invalid"},
	} {
		req.Kind, req.Version = sendStatusKind, sendDelegateVersion
		if _, err := executeDelegatedSend(context.Background(), nil, req); err == nil {
			t.Fatal("invalid status accepted")
		}
	}
}

func TestStatusReadOnlyDoesNotDelegate(t *testing.T) {
	_, stderr, err := runStatusCLI(t, []string{"--store", t.TempDir(), "--read-only", "send", "status", "--message", "synthetic"})
	if err == nil || !strings.Contains(stderr, "read-only") {
		t.Fatalf("err=%v stderr=%s", err, stderr)
	}
}
