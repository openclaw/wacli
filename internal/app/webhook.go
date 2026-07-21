package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/linkpreview"
	"github.com/openclaw/wacli/internal/wa"
)

var syncWebhookPrivateHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var syncWebhookSafeHTTPClient = newSyncWebhookSafeHTTPClient()

var syncWebhookRequestTimeout = 5 * time.Second

type syncWebhookPayload struct {
	wa.ParsedMessage
	ChatName string `json:"ChatName,omitempty"`
}

func newSyncWebhookSafeHTTPClient() *http.Client {
	client := linkpreview.NewSafeHTTPClient()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

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
	ctx, cancel := context.WithTimeout(ctx, syncWebhookRequestTimeout)
	defer cancel()
	payload, err := json.Marshal(a.newSyncWebhookPayload(ctx, pm))
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}
	req, err := newSyncWebhookRequest(ctx, webhookURL, opts.WebhookSecret, a.Version(), payload)
	if err != nil {
		return err
	}
	client := syncWebhookSafeHTTPClient
	if opts.WebhookAllowPrivate {
		client = syncWebhookPrivateHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook %s: %s", redactedWebhookURL(webhookURL), redactWebhookError(webhookURL, err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("post webhook: %s", resp.Status)
	}
	return nil
}

func (a *App) newSyncWebhookPayload(ctx context.Context, pm wa.ParsedMessage) syncWebhookPayload {
	payload := syncWebhookPayload{ParsedMessage: pm}
	chatJID := canonicalJIDString(pm.Chat)
	if a.wa != nil {
		chatJID = canonicalJIDString(a.canonicalStoreJID(ctx, pm.Chat))
	}
	if chatJID != "" && a.db != nil {
		chat, err := a.db.GetChat(chatJID)
		if err != nil {
			return payload
		}
		payload.ChatName = chat.Name
	}
	return payload
}

func newSyncWebhookRequest(ctx context.Context, webhookURL, secret, version string, payload []byte) (*http.Request, error) {
	if err := validateWebhookURL(webhookURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create webhook request %s: %s", redactedWebhookURL(webhookURL), redactWebhookError(webhookURL, err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wacli/"+version)
	if strings.TrimSpace(secret) != "" {
		req.Header.Set("X-Wacli-Signature", syncWebhookSignature(secret, payload))
	}
	return req, nil
}

func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid webhook URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URL must use http or https")
	}
	return nil
}

func redactedWebhookURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid-url>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = ""
	return u.String()
}

func redactWebhookError(_ string, err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Op != "" {
		return urlErr.Op + " failed"
	}
	return "request failed"
}

func syncWebhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
