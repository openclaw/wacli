package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
)

// Only protocol calls are fake; the index, persisted recovered path and cache
// verification are real. Unimplemented WA methods are never used in this flow.
type pipelineWA struct {
	app.WAClient
	downloadErr error
	downloads   int
}

func (f *pipelineWA) IsConnected() bool { return true }
func (f *pipelineWA) DownloadMediaToFile(context.Context, string, []byte, []byte, []byte, uint64, string, string, string) (int64, error) {
	f.downloads++
	return 0, f.downloadErr
}

type pipelineApp struct {
	*app.App
	client        *pipelineWA
	phoneRequests int
	content       []byte
}

func (a *pipelineApp) WA() app.WAClient { return a.client }
func (a *pipelineApp) RetryMediaMessage(ctx context.Context, chat, id string, wait time.Duration) (app.MediaRetryOutcome, error) {
	a.phoneRequests++
	if wait != 30*time.Second {
		return app.MediaRetryOutcome{}, errors.New("unexpected phone wait")
	}
	info, err := a.DB().GetMediaDownloadInfo(chat, id)
	if err != nil {
		return app.MediaRetryOutcome{}, err
	}
	path, err := a.ResolveMediaOutputPath(info, "")
	if err != nil {
		return app.MediaRetryOutcome{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return app.MediaRetryOutcome{}, err
	}
	if err := os.WriteFile(path, a.content, 0o600); err != nil {
		return app.MediaRetryOutcome{}, err
	}
	if err := a.DB().MarkMediaDownloaded(chat, id, path, time.Now()); err != nil {
		return app.MediaRetryOutcome{}, err
	}
	return app.MediaRetryOutcome{ChatJID: chat, MsgID: id, Status: "recovered", Path: path, Bytes: int64(len(a.content))}, nil
}

func TestExactMediaDownloadPhoneFallbackOnlyForExpiredResponses(t *testing.T) {
	for _, status := range []struct {
		name     string
		err      error
		eligible bool
	}{
		{"403", whatsmeow.ErrMediaDownloadFailedWith403, true},
		{"404", whatsmeow.ErrMediaDownloadFailedWith404, true},
		{"410", whatsmeow.ErrMediaDownloadFailedWith410, true},
		{"network", errors.New("temporary network failure"), false},
		{"integrity", whatsmeow.ErrInvalidMediaSHA256, false},
	} {
		for _, enabled := range []bool{false, true} {
			name := status.name + map[bool]string{false: "-disabled", true: "-enabled"}[enabled]
			t.Run(name, func(t *testing.T) {
				base, err := app.New(app.Options{StoreDir: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				defer base.Close()
				data := []byte("exact synthetic phone recovery")
				hash := sha256.Sum256(data)
				chat, id := "111@s.whatsapp.net", "target"
				if err := base.DB().UpsertChat(chat, "dm", "Synthetic", time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := base.DB().UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: id, SenderJID: chat, Timestamp: time.Now(), MediaType: "audio", DirectPath: "/expired", MediaKey: make([]byte, 32), FileSHA256: hash[:], FileLength: uint64(len(data))}); err != nil {
					t.Fatal(err)
				}
				a := &pipelineApp{App: base, client: &pipelineWA{downloadErr: status.err}, content: data}
				result, err := executeExactMediaDownload(context.Background(), a, chat, id, enabled)
				if !enabled || !status.eligible {
					if !errors.Is(err, status.err) || a.phoneRequests != 0 {
						t.Fatalf("unexpected fallback: result=%+v err=%v requests=%d", result, err, a.phoneRequests)
					}
					return
				}
				if err != nil || result.Source != "phone" || a.phoneRequests != 1 {
					t.Fatalf("recovery result=%+v err=%v requests=%d", result, err, a.phoneRequests)
				}
				// The original server still fails, but repeated retrieval must now
				// reuse the recovered bytes without another download/phone request.
				again, err := executeExactMediaDownload(context.Background(), a, chat, id, enabled)
				if err != nil || again.Source != "cache" || a.phoneRequests != 1 || a.client.downloads != 1 {
					t.Fatalf("repeat result=%+v err=%v requests=%d downloads=%d", again, err, a.phoneRequests, a.client.downloads)
				}
			})
		}
	}
}
