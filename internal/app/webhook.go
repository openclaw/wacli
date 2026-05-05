package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/steipete/wacli/internal/wa"
)

var syncWebhookHTTPClient = &http.Client{Timeout: 10 * time.Second}

func syncWebhookEnabled(opts SyncOptions) bool {
	return strings.TrimSpace(opts.WebhookURL) != ""
}

func (a *App) newSyncWebhookEnqueuer(ctx context.Context, jobs chan<- wa.ParsedMessage) func(wa.ParsedMessage) {
	return func(pm wa.ParsedMessage) {
		if strings.TrimSpace(pm.ID) == "" {
			return
		}
		select {
		case jobs <- pm:
		case <-ctx.Done():
		default:
			a.emitWarning(
				"sync_webhook_dropped",
				fmt.Sprintf("warning: sync webhook queue full; dropping message %s", pm.ID),
				map[string]any{"message_id": pm.ID},
			)
		}
	}
}

func (a *App) runSyncWebhookWorker(ctx context.Context, opts SyncOptions, jobs <-chan wa.ParsedMessage) func() {
	if jobs == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case pm, ok := <-jobs:
				if !ok {
					return
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							stack := debug.Stack()
							a.emitWarning(
								"sync_webhook_panic",
								fmt.Sprintf("sync webhook worker panic (recovered) for %s: %v\n%s", pm.ID, r, stack),
								map[string]any{"message_id": pm.ID, "panic": fmt.Sprint(r), "stack": string(stack)},
							)
						}
					}()
					if err := a.postSyncWebhook(ctx, opts, pm); err != nil {
						a.emitWarning(
							"sync_webhook_failed",
							fmt.Sprintf("warning: sync webhook failed for message %s: %v", pm.ID, err),
							map[string]any{"message_id": pm.ID, "error": err.Error()},
						)
					}
				}()
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

func (a *App) postSyncWebhook(ctx context.Context, opts SyncOptions, pm wa.ParsedMessage) error {
	webhookURL := strings.TrimSpace(opts.WebhookURL)
	if webhookURL == "" {
		return nil
	}
	payload, err := json.Marshal(pm)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	req, err := newSyncWebhookRequest(ctx, webhookURL, opts.WebhookSecret, a.Version(), payload)
	if err != nil {
		return err
	}
	resp, err := syncWebhookHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("post webhook: %s", resp.Status)
	}
	return nil
}

func newSyncWebhookRequest(ctx context.Context, webhookURL, secret, version string, payload []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wacli/"+version)
	if strings.TrimSpace(secret) != "" {
		req.Header.Set("X-Wacli-Signature", syncWebhookSignature(secret, payload))
	}
	return req, nil
}

func syncWebhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
