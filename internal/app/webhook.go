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
	"sync/atomic"
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

type syncWebhookReceiptPayload struct {
	EventType SyncWebhookEventKind `json:"EventType"`
	syncWebhookReceipt
}

type syncWebhookChatPresencePayload struct {
	EventType SyncWebhookEventKind `json:"EventType"`
	syncWebhookChatPresence
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

func (a *App) newSyncWebhookEnqueuer(ctx context.Context, jobs chan<- syncWebhookEvent) func(syncWebhookEvent) {
	var dropped atomic.Int64
	return func(evt syncWebhookEvent) {
		if evt.Kind == "" {
			evt.Kind = SyncWebhookEventMessage
		}
		if evt.Kind == SyncWebhookEventMessage && strings.TrimSpace(evt.Message.ID) == "" {
			return
		}
		select {
		case jobs <- evt:
		case <-ctx.Done():
		default:
			// A dropped message is caught by reconciliation, but a dropped
			// receipt is a tick that never advances and leaves no trace, so
			// report the kind and a running total.
			total := dropped.Add(1)
			fields := evt.logFields()
			fields["dropped"] = total
			a.emitWarning(
				"sync_webhook_dropped",
				fmt.Sprintf("warning: sync webhook queue full; dropping %s %s (dropped=%d)", evt.Kind, evt.id(), total),
				fields,
			)
		}
	}
}

// newSyncWebhookMessageEnqueuer adapts the event enqueuer to the message-only
// signature used by the live message path.
func newSyncWebhookMessageEnqueuer(enqueue func(syncWebhookEvent)) func(wa.ParsedMessage) {
	return func(pm wa.ParsedMessage) {
		enqueue(syncWebhookEvent{Kind: SyncWebhookEventMessage, Message: pm})
	}
}

func (a *App) runSyncWebhookWorker(ctx context.Context, opts SyncOptions, jobs <-chan syncWebhookEvent) func() {
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
			case evt, ok := <-jobs:
				if !ok {
					return
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							stack := debug.Stack()
							fields := evt.logFields()
							fields["panic"] = fmt.Sprint(r)
							fields["stack"] = string(stack)
							a.emitWarning(
								"sync_webhook_panic",
								fmt.Sprintf("sync webhook worker panic (recovered) for %s: %v\n%s", evt.id(), r, stack),
								fields,
							)
						}
					}()
					if err := a.postSyncWebhookEvent(ctx, opts, evt); err != nil {
						fields := evt.logFields()
						fields["error"] = err.Error()
						a.emitWarning(
							"sync_webhook_failed",
							fmt.Sprintf("warning: sync webhook failed for %s %s: %v", evt.Kind, evt.id(), err),
							fields,
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

func (a *App) postSyncWebhookEvent(ctx context.Context, opts SyncOptions, evt syncWebhookEvent) error {
	webhookURL := strings.TrimSpace(opts.WebhookURL)
	if webhookURL == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, syncWebhookRequestTimeout)
	defer cancel()
	payload, err := json.Marshal(a.newSyncWebhookEventPayload(ctx, evt))
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

func (a *App) newSyncWebhookEventPayload(ctx context.Context, evt syncWebhookEvent) any {
	switch evt.Kind {
	case SyncWebhookEventReceipt:
		return syncWebhookReceiptPayload{EventType: SyncWebhookEventReceipt, syncWebhookReceipt: evt.Receipt}
	case SyncWebhookEventChatPresence:
		return syncWebhookChatPresencePayload{EventType: SyncWebhookEventChatPresence, syncWebhookChatPresence: evt.Presence}
	default:
		return a.newSyncWebhookPayload(ctx, evt.Message)
	}
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
