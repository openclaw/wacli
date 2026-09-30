package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"go.mau.fi/whatsmeow/types"
)

func TestPresenceMediaFromString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    types.ChatPresenceMedia
		wantErr bool
	}{
		{name: "empty", input: "", want: ""},
		{name: "audio", input: "audio", want: types.ChatPresenceMediaAudio},
		{name: "trimmed case", input: " Audio ", want: types.ChatPresenceMediaAudio},
		{name: "unknown", input: "video", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := presenceMediaFromString(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("presenceMediaFromString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPresenceStateFromString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    types.ChatPresence
		wantErr bool
	}{
		{name: "composing", input: "composing", want: types.ChatPresenceComposing},
		{name: "paused", input: "paused", want: types.ChatPresencePaused},
		{name: "trimmed case", input: " Paused ", want: types.ChatPresencePaused},
		{name: "unknown", input: "available", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := presenceStateFromString(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("presenceStateFromString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

const (
	presenceDelegateHelperEnv     = "WACLI_PRESENCE_DELEGATE_HELPER"
	presenceDelegateHelperArgsEnv = "WACLI_PRESENCE_DELEGATE_ARGS"
)

func TestPresenceDelegateHelper(t *testing.T) {
	if os.Getenv(presenceDelegateHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(presenceDelegateHelperArgsEnv)), &args); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "decode helper args: %v\n", err)
		os.Exit(2)
	}
	if err := execute(args); err != nil {
		os.Exit(1)
	}
}

func TestPresenceDelegatesThroughSendSocketWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)

	tests := []struct {
		name      string
		command   string
		media     string
		wantState string
		wantMedia string
	}{
		{name: "typing", command: "typing", media: "audio", wantState: string(types.ChatPresenceComposing), wantMedia: "audio"},
		{name: "paused", command: "paused", wantState: string(types.ChatPresencePaused)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(storeDir)
			if err != nil {
				t.Fatalf("lock store: %v", err)
			}
			defer lk.Release()

			server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
				return sendDelegateResponse{OK: true, Sent: true, To: "15551234567@s.whatsapp.net"}
			})
			defer server.stop()

			args := []string{"--store", storeDir, "--json", "--timeout", "750ms", "presence", tt.command, "--to", "+1 (555) 123-4567"}
			if tt.media != "" {
				args = append(args, "--media", tt.media)
			}
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err != nil {
				t.Fatalf("presence command failed: %v stdout=%q stderr=%q", err, stdout, stderr)
			}

			req := server.nextRequest(t)
			if req.Version != sendDelegateVersion {
				t.Fatalf("delegate version = %d, want %d", req.Version, sendDelegateVersion)
			}
			if req.Kind != "presence" {
				t.Fatalf("delegate kind = %q, want presence", req.Kind)
			}
			if req.To != "+1 (555) 123-4567" {
				t.Fatalf("delegate to = %q", req.To)
			}
			if req.PresenceState != tt.wantState {
				t.Fatalf("delegate presence state = %q, want %q", req.PresenceState, tt.wantState)
			}
			if req.PresenceMedia != tt.wantMedia {
				t.Fatalf("delegate presence media = %q, want %q", req.PresenceMedia, tt.wantMedia)
			}
			if req.TimeoutMS != 750 {
				t.Fatalf("delegate timeout_ms = %d, want 750", req.TimeoutMS)
			}
			if strings.Contains(stderr, "store is locked") || strings.Contains(stderr, "not authenticated") || strings.Contains(stderr, "not connected") {
				t.Fatalf("delegated command tried the direct store/client path: stderr=%q", stderr)
			}

			jsonLine := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
			var payload struct {
				Success bool `json:"success"`
				Data    struct {
					Sent  bool   `json:"sent"`
					To    string `json:"to"`
					State string `json:"state"`
				} `json:"data"`
				Error *string `json:"error"`
			}
			if err := json.Unmarshal([]byte(jsonLine), &payload); err != nil {
				t.Fatalf("decode stdout JSON %q: %v", stdout, err)
			}
			if !payload.Success || payload.Error != nil || !payload.Data.Sent || payload.Data.To != "15551234567@s.whatsapp.net" || payload.Data.State != tt.wantState {
				t.Fatalf("stdout payload = %+v", payload)
			}
		})
	}
}

func TestPresenceLockedStoreReturnsLockErrorWithoutDelegateSocket(t *testing.T) {
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "750ms", "presence", "typing", "--to", "+15551234567"})
	if err == nil {
		t.Fatalf("presence command unexpectedly succeeded: stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "store is locked") {
		t.Fatalf("stderr = %q, want original lock error", stderr)
	}
	if strings.Contains(stderr, "send delegate unavailable") {
		t.Fatalf("stderr leaked delegate fallback error: %q", stderr)
	}
}

func TestPresenceDelegatePropagatesDaemonErrorWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)

	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: false, Error: "daemon rejected presence"}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "750ms", "presence", "paused", "--to", "+15551234567"})
	if err == nil {
		t.Fatalf("presence command unexpectedly succeeded: stdout=%q stderr=%q", stdout, stderr)
	}
	_ = server.nextRequest(t)
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "daemon rejected presence") {
		t.Fatalf("stderr = %q, want daemon error", stderr)
	}
	if strings.Contains(stderr, "store is locked") {
		t.Fatalf("stderr returned lock error instead of daemon error: %q", stderr)
	}
}

func TestExecuteDelegatedSendRoutesPresenceValidation(t *testing.T) {
	_, err := executeDelegatedSend(contextWithTestTimeout(t), nil, sendDelegateRequest{
		Version:       sendDelegateVersion,
		Kind:          "presence",
		To:            "+15551234567",
		PresenceState: "available",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported presence state") {
		t.Fatalf("executeDelegatedSend error = %v, want presence state validation", err)
	}
}

func TestSendPresenceWithRetryReconnectsOnNotConnected(t *testing.T) {
	var sends int
	var reconnects int
	err := sendPresenceWithRetry(contextWithTestTimeout(t), func(context.Context) error {
		reconnects++
		return nil
	}, func(context.Context) error {
		sends++
		if sends == 1 {
			return errors.New("not connected")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("sendPresenceWithRetry: %v", err)
	}
	if sends != 2 {
		t.Fatalf("send attempts = %d, want 2", sends)
	}
	if reconnects != 1 {
		t.Fatalf("reconnects = %d, want 1", reconnects)
	}
}

func TestPresenceSubscribeRejectsInvalidFlagsBeforeStoreAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no contact", args: nil, want: "--to is required"},
		{name: "negative wait", args: []string{"--to", "+15551234567", "--wait", "-1s"}, want: "--wait must be >= 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--account", "does-not-exist", "presence", "subscribe"}, tc.args...)
			err := execute(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("execute error = %v, want %q", err, tc.want)
			}
		})
	}
}

// With no sync running, the command's own connection would close as it
// returns, ending the subscription: --wait 0 is refused rather than reported
// as a watch, before anything connects.
func TestPresenceSubscribeWithoutWaitNeedsARunningSync(t *testing.T) {
	storeDir := t.TempDir()
	err := execute([]string{"--store", storeDir, "presence", "subscribe", "--to", "+15551234567", "--wait", "0"})
	if err == nil || !strings.Contains(err.Error(), "running `wacli sync --follow`") {
		t.Fatalf("execute error = %v, want the running-sync requirement", err)
	}
}

func TestPresenceSubscribeDelegatesThroughSendSocketWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)

	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, To: "15551234567@s.whatsapp.net",
			Presence: &app.PresenceState{JID: "15551234567@s.whatsapp.net", Online: true}}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--json", "--timeout", "750ms",
		"presence", "subscribe", "--to", "+1 (555) 123-4567", "--wait", "500ms"})
	if err != nil {
		t.Fatalf("presence subscribe failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}

	req := server.nextRequest(t)
	if req.Kind != presenceSubscribeKind || req.To != "+1 (555) 123-4567" || req.PresenceWaitMS != 500 {
		t.Fatalf("delegate request = %+v, want %s for the contact with a 500 ms wait", req, presenceSubscribeKind)
	}
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Subscribed bool               `json:"subscribed"`
			To         string             `json:"to"`
			Presence   *app.PresenceState `json:"presence"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]), &payload); err != nil {
		t.Fatalf("decode stdout JSON %q: %v", stdout, err)
	}
	if !payload.Success || !payload.Data.Subscribed || payload.Data.Presence == nil || !payload.Data.Presence.Online {
		t.Fatalf("stdout payload = %+v", payload)
	}
}

type fakePresenceWatchApp struct {
	jid   types.JID
	wait  time.Duration
	state *app.PresenceState
	err   error
}

func (f *fakePresenceWatchApp) WatchPresence(_ context.Context, jid types.JID, wait time.Duration) (*app.PresenceState, error) {
	f.jid, f.wait = jid, wait
	return f.state, f.err
}

func TestExecuteDelegatedPresenceSubscribe(t *testing.T) {
	online := &app.PresenceState{JID: "15551234567@s.whatsapp.net", Online: true}
	fake := &fakePresenceWatchApp{state: online}
	resp, err := executeDelegatedPresenceSubscribe(contextWithTestTimeout(t), fake, sendDelegateRequest{
		Kind: presenceSubscribeKind, To: "+1 (555) 123-4567", PresenceWaitMS: 2500,
	})
	if err != nil {
		t.Fatalf("executeDelegatedPresenceSubscribe: %v", err)
	}
	if fake.jid.String() != "15551234567@s.whatsapp.net" || fake.wait != 2500*time.Millisecond {
		t.Fatalf("watched %s for %s, want 15551234567@s.whatsapp.net for 2.5s", fake.jid, fake.wait)
	}
	if !resp.OK || resp.To != "15551234567@s.whatsapp.net" || resp.Presence != online {
		t.Fatalf("response = %+v", resp)
	}

	if _, err := executeDelegatedPresenceSubscribe(contextWithTestTimeout(t), fake, sendDelegateRequest{
		Kind: presenceSubscribeKind, To: "+15551234567", PresenceWaitMS: -1,
	}); err == nil {
		t.Fatalf("a negative wait was accepted")
	}
	quiet := &fakePresenceWatchApp{err: app.ErrPresenceQuiet}
	if _, err := executeDelegatedPresenceSubscribe(contextWithTestTimeout(t), quiet, sendDelegateRequest{
		Kind: presenceSubscribeKind, To: "+15551234567",
	}); !errors.Is(err, app.ErrPresenceQuiet) {
		t.Fatalf("error = %v, want the quiet sync's refusal", err)
	}
}

// A presence subscription can wait seconds for WhatsApp's answer. It must not
// queue behind a send that holds the delegate's send slot (a large upload), nor
// take that slot itself.
func TestPresenceSubscribeIsNotQueuedBehindSends(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}

	sendSlot := make(chan struct{}, 1) // empty: a send is in progress and owns it
	executed := make(chan sendDelegateRequest, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSendDelegateConn(context.Background(), serverConn, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
			executed <- req
			return sendDelegateResponse{OK: true, To: req.To}, nil
		}, sendSlot, newSendPacer(sendSpacing{}))
	}()

	if err := json.NewEncoder(clientConn).Encode(sendDelegateRequest{Version: sendDelegateVersion, Kind: presenceSubscribeKind, To: "x"}); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("decode response (queued behind the send?): %v", err)
	}
	if !resp.OK || len(executed) != 1 {
		t.Fatalf("response = %+v, executed %d", resp, len(executed))
	}
	<-done
}

type presenceDelegateTestServer struct {
	requests chan presenceDelegateTestRequest
	stop     func()
}

type presenceDelegateTestRequest struct {
	req sendDelegateRequest
	err error
}

func startPresenceDelegateTestSocket(t *testing.T, storeDir string, respond func(sendDelegateRequest) sendDelegateResponse) *presenceDelegateTestServer {
	t.Helper()
	ln, err := net.Listen("unix", sendDelegateSocketPath(storeDir))
	if err != nil {
		t.Fatalf("listen delegate socket: %v", err)
	}
	if err := os.Chmod(sendDelegateSocketPath(storeDir), 0o600); err != nil {
		_ = ln.Close()
		t.Fatalf("chmod delegate socket: %v", err)
	}
	requests := make(chan presenceDelegateTestRequest, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

		var req sendDelegateRequest
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			requests <- presenceDelegateTestRequest{err: err}
			return
		}
		requests <- presenceDelegateTestRequest{req: req}
		_ = json.NewEncoder(conn).Encode(respond(req))
	}()

	return &presenceDelegateTestServer{
		requests: requests,
		stop: func() {
			_ = ln.Close()
			<-done
			_ = os.Remove(sendDelegateSocketPath(storeDir))
		},
	}
}

func (s *presenceDelegateTestServer) nextRequest(t *testing.T) sendDelegateRequest {
	t.Helper()
	select {
	case got := <-s.requests:
		if got.err != nil {
			t.Fatalf("decode delegate request: %v", got.err)
		}
		return got.req
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delegate request")
		return sendDelegateRequest{}
	}
}

func runPresenceDelegateHelper(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal helper args: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPresenceDelegateHelper$")
	cmd.Env = append(os.Environ(),
		presenceDelegateHelperEnv+"=1",
		presenceDelegateHelperArgsEnv+"="+string(rawArgs),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func shortPresenceDelegateStoreDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if runtime.GOOS == "darwin" {
		// Darwin has a short sockaddr_un path limit; the default TMPDIR under
		// /var/folders is often too long for .send.sock integration tests.
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "wacli-presence-*")
	if err != nil {
		t.Fatalf("temp store dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func skipPresenceDelegateSocketTestOnUnsupportedOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix send delegate socket integration is not available on Windows")
	}
}

func contextWithTestTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
