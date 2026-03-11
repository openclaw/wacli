package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sendDelegateProtocolVersion is the wire protocol version for IPC send delegation.
const sendDelegateProtocolVersion = 1

// SendSocketPath returns the Unix socket path for send delegation.
func SendSocketPath(storeDir string) string {
	return filepath.Join(storeDir, ".send.sock")
}

// ErrSendDelegateUnavailable indicates that send delegation is not available
// (missing socket, connection refused, or transport failure).
var ErrSendDelegateUnavailable = errors.New("send delegate unavailable")

// SendDelegateRemoteError represents an application error that occurred
// on the server side during a delegated send.
type SendDelegateRemoteError struct {
	Message string
}

func (e *SendDelegateRemoteError) Error() string {
	return fmt.Sprintf("remote send error: %s", e.Message)
}

// SendDelegateProtocolError represents a protocol version mismatch or
// invalid protocol state.
type SendDelegateProtocolError struct {
	Message         string
	ExpectedVersion int
	ActualVersion   int
}

func (e *SendDelegateProtocolError) Error() string {
	if e.ExpectedVersion != 0 || e.ActualVersion != 0 {
		return fmt.Sprintf("send delegate protocol error: %s (expected version %d, got %d)", e.Message, e.ExpectedVersion, e.ActualVersion)
	}
	return fmt.Sprintf("send delegate protocol error: %s", e.Message)
}

// --- Protocol types (private) ---

type sendDelegateOp string

const (
	sendDelegateOpText sendDelegateOp = "send_text"
	sendDelegateOpFile sendDelegateOp = "send_file"
)

type sendDelegateRequest struct {
	Version   int              `json:"version"`
	Op        sendDelegateOp   `json:"op"`
	TimeoutMS int64            `json:"timeout_ms,omitempty"`
	Text      *SendTextParams  `json:"text,omitempty"`
	File      *SendFileParams  `json:"file,omitempty"`
}

type sendDelegateResponse struct {
	Version int                        `json:"version"`
	Result  *SendResult                `json:"result,omitempty"`
	Error   *sendDelegateErrorPayload  `json:"error,omitempty"`
}

type sendDelegateErrorPayload struct {
	Kind            string `json:"kind"` // "remote" or "protocol"
	Message         string `json:"message"`
	ExpectedVersion int    `json:"expected_version,omitempty"`
	ActualVersion   int    `json:"actual_version,omitempty"`
}

// --- Client ---

// DelegateSendText sends a text message via the IPC socket of a running sync process.
func DelegateSendText(ctx context.Context, storeDir string, params SendTextParams) (SendResult, error) {
	return delegateSend(ctx, storeDir, sendDelegateRequest{
		Version: sendDelegateProtocolVersion,
		Op:      sendDelegateOpText,
		Text:    &params,
	})
}

// DelegateSendFile sends a file via the IPC socket of a running sync process.
func DelegateSendFile(ctx context.Context, storeDir string, params SendFileParams) (SendResult, error) {
	return delegateSend(ctx, storeDir, sendDelegateRequest{
		Version: sendDelegateProtocolVersion,
		Op:      sendDelegateOpFile,
		File:    &params,
	})
}

func delegateSend(ctx context.Context, storeDir string, req sendDelegateRequest) (SendResult, error) {
	sockPath := SendSocketPath(storeDir)

	// Derive TimeoutMS from context deadline.
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining <= 0 {
			return SendResult{}, ctx.Err()
		}
		ms := remaining.Milliseconds()
		if ms < 1 {
			ms = 1
		}
		req.TimeoutMS = ms
	}

	// Dial the Unix socket.
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return SendResult{}, fmt.Errorf("%w: %v", ErrSendDelegateUnavailable, err)
	}
	defer conn.Close()

	// Propagate deadline to the connection.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	// Encode request.
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return SendResult{}, fmt.Errorf("%w: encode request: %v", ErrSendDelegateUnavailable, err)
	}

	// Decode response.
	var resp sendDelegateResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return SendResult{}, fmt.Errorf("%w: decode response: %v", ErrSendDelegateUnavailable, err)
	}

	// Validate response version.
	if resp.Version != sendDelegateProtocolVersion {
		return SendResult{}, &SendDelegateProtocolError{
			Message:         "version mismatch",
			ExpectedVersion: sendDelegateProtocolVersion,
			ActualVersion:   resp.Version,
		}
	}

	// Handle error responses.
	if resp.Error != nil {
		switch resp.Error.Kind {
		case "protocol":
			return SendResult{}, &SendDelegateProtocolError{
				Message:         resp.Error.Message,
				ExpectedVersion: resp.Error.ExpectedVersion,
				ActualVersion:   resp.Error.ActualVersion,
			}
		default:
			return SendResult{}, &SendDelegateRemoteError{
				Message: resp.Error.Message,
			}
		}
	}

	if resp.Result == nil {
		return SendResult{}, fmt.Errorf("%w: empty result in response", ErrSendDelegateUnavailable)
	}

	return *resp.Result, nil
}

// --- Server ---

type sendDelegateServer struct {
	app       *App
	path      string
	listener  *net.UnixListener
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

// startSendDelegateServer starts the IPC server for send delegation.
// The server accepts connections sequentially and dispatches to App.SendText/SendFile.
// The caller must call Close() when finished.
func (a *App) startSendDelegateServer() (*sendDelegateServer, error) {
	sockPath := SendSocketPath(a.opts.StoreDir)

	// Clean up stale socket. Only remove if it is actually a socket.
	if info, err := os.Lstat(sockPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("send socket path exists but is not a socket: %s", sockPath)
		}
		if err := os.Remove(sockPath); err != nil {
			return nil, fmt.Errorf("remove stale send socket: %w", err)
		}
	}

	addr := &net.UnixAddr{Name: sockPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listen send socket: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	s := &sendDelegateServer{
		app:      a,
		path:     sockPath,
		listener: listener,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}

	go s.serve()
	return s, nil
}

// Close stops the server, waits for the serve loop to exit, and removes the socket.
// It is safe to call multiple times.
func (s *sendDelegateServer) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		s.cancel()
		firstErr = s.listener.Close()
		<-s.done
		_ = os.Remove(s.path)
	})
	return firstErr
}

func (s *sendDelegateServer) serve() {
	defer close(s.done)
	defer func() { _ = os.Remove(s.path) }()

	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
			}
			continue
		}
		s.handleConn(conn)
	}
}

func (s *sendDelegateServer) handleConn(conn *net.UnixConn) {
	defer conn.Close()

	var req sendDelegateRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	resp := s.handleRequest(req)
	_ = json.NewEncoder(conn).Encode(resp)
}

func (s *sendDelegateServer) handleRequest(req sendDelegateRequest) sendDelegateResponse {
	// Validate protocol version.
	if req.Version != sendDelegateProtocolVersion {
		return sendDelegateResponse{
			Version: sendDelegateProtocolVersion,
			Error: &sendDelegateErrorPayload{
				Kind:            "protocol",
				Message:         "unsupported protocol version",
				ExpectedVersion: sendDelegateProtocolVersion,
				ActualVersion:   req.Version,
			},
		}
	}

	// Build request context with optional timeout.
	reqCtx := s.ctx
	var cancel context.CancelFunc
	if req.TimeoutMS > 0 {
		reqCtx, cancel = context.WithTimeout(s.ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
	} else {
		reqCtx, cancel = context.WithCancel(s.ctx)
	}
	defer cancel()

	var result SendResult
	var err error

	switch req.Op {
	case sendDelegateOpText:
		if req.Text == nil {
			return sendDelegateResponse{
				Version: sendDelegateProtocolVersion,
				Error: &sendDelegateErrorPayload{
					Kind:    "protocol",
					Message: "missing text params for send_text operation",
				},
			}
		}
		result, err = s.app.SendText(reqCtx, *req.Text)
	case sendDelegateOpFile:
		if req.File == nil {
			return sendDelegateResponse{
				Version: sendDelegateProtocolVersion,
				Error: &sendDelegateErrorPayload{
					Kind:    "protocol",
					Message: "missing file params for send_file operation",
				},
			}
		}
		result, err = s.app.SendFile(reqCtx, *req.File)
	default:
		return sendDelegateResponse{
			Version: sendDelegateProtocolVersion,
			Error: &sendDelegateErrorPayload{
				Kind:    "protocol",
				Message: fmt.Sprintf("unknown operation: %s", req.Op),
			},
		}
	}

	if err != nil {
		return sendDelegateResponse{
			Version: sendDelegateProtocolVersion,
			Error: &sendDelegateErrorPayload{
				Kind:    "remote",
				Message: err.Error(),
			},
		}
	}

	return sendDelegateResponse{
		Version: sendDelegateProtocolVersion,
		Result:  &result,
	}
}
