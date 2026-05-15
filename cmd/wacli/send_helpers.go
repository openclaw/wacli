package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/fsutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

const sendAttemptTimeout = 45 * time.Second
const postSendRetryReceiptWait = 2 * time.Second
const rapidSendWarningThreshold = 5 * time.Second
const lastSendAttemptFile = ".last-send-at"

func runSendOperation[T any](
	ctx context.Context,
	reconnect func(context.Context) error,
	op func(context.Context) (T, error),
) (T, error) {
	result, err := runSendAttempt(ctx, sendAttemptTimeout, op)
	if err == nil {
		return result, nil
	}

	var zero T
	if !isRetryableSendError(err) || ctx.Err() != nil {
		return zero, err
	}
	if reconnectErr := reconnect(ctx); reconnectErr != nil {
		return zero, fmt.Errorf("%w; reconnect failed: %v", err, reconnectErr)
	}
	return runSendAttempt(ctx, sendAttemptTimeout, op)
}

func runSendAttempt[T any](ctx context.Context, timeout time.Duration, op func(context.Context) (T, error)) (T, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		value T
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		value, err := op(attemptCtx)
		ch <- result{value: value, err: err}
	}()

	select {
	case res := <-ch:
		if errors.Is(res.err, context.DeadlineExceeded) && errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			var zero T
			return zero, fmt.Errorf("send timed out after %s", timeout)
		}
		return res.value, res.err
	case <-attemptCtx.Done():
		var zero T
		if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			return zero, fmt.Errorf("send timed out after %s", timeout)
		}
		return zero, attemptCtx.Err()
	}
}

func isRetryableSendError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, whatsmeow.ErrIQTimedOut) {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "failed to send usync query") ||
		strings.Contains(msg, "failed to get user info") ||
		strings.Contains(msg, "failed to get device list") ||
		strings.Contains(msg, "info query timed out") ||
		strings.Contains(msg, "not connected")
}

func reconnectForSend(a interface {
	WA() app.WAClient
	Connect(context.Context, bool, func(string)) error
}) func(context.Context) error {
	return func(ctx context.Context) error {
		a.WA().Close()
		return a.Connect(ctx, false, nil)
	}
}

func waitForPostSendRetryReceipts(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func warnRapidSendIfNeeded(storeDir string, now time.Time, stderr io.Writer) error {
	path := filepath.Join(storeDir, lastSendAttemptFile)
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read last send marker: %w", err)
	}
	if err == nil {
		last, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
		if parseErr == nil {
			if elapsed := now.Sub(last); elapsed >= 0 && elapsed < rapidSendWarningThreshold {
				fmt.Fprintf(stderr, "warning: send command was invoked %s after the previous send; rapid automated sends may trigger WhatsApp rate limits or account restrictions\n", elapsed.Round(time.Second))
			}
		}
	}
	if err := fsutil.WritePrivateFile(path, []byte(now.Format(time.RFC3339Nano)+"\n")); err != nil {
		return fmt.Errorf("write last send marker: %w", err)
	}
	return nil
}

// warmupRecipient canonicalizes direct phone-number recipients and resolves
// user info before sending to establish contact state with WhatsApp's servers.
// Without this, the privacy token (tctoken) IQ may fail with 400: bad-request
// for new or unknown contacts, causing messages to be silently dropped despite
// returning sent:true.
//
// The warmup is best-effort: failures are logged to stderr but never
// block the send. The returned JID is the canonical send target when WhatsApp
// registration lookup resolves one.
//
// userInfoResolver is satisfied by app.WAClient.
type userInfoResolver interface {
	GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error)
}

type whatsappRegistrationResolver interface {
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
}

func warmupRecipient(ctx context.Context, wa userInfoResolver, jid types.JID, stderr io.Writer) types.JID {
	if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return jid
	}
	if jid.Server == types.DefaultUserServer {
		if resolver, ok := any(wa).(whatsappRegistrationResolver); ok {
			registrations, regErr := resolver.IsOnWhatsApp(ctx, []string{"+" + jid.User})
			if regErr != nil {
				fmt.Fprintf(stderr, "warn: send registration warmup for %s failed (send will proceed): %v\n", jid, regErr)
			} else if len(registrations) > 0 && registrations[0].IsIn && !registrations[0].JID.IsEmpty() {
				jid = registrations[0].JID.ToNonAD()
			}
		}
	}
	_, err := wa.GetUserInfo(ctx, []types.JID{jid})
	if err != nil {
		fmt.Fprintf(stderr, "warn: send warmup for %s failed (send will proceed): %v\n", jid, err)
	}
	return jid
}
