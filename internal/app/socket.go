package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

// SocketRequest is a JSON request sent over the Unix domain socket.
type SocketRequest struct {
	Action string `json:"action"` // "send_text", "send_file", "mark_read", "ping"

	// send_text fields
	To      string `json:"to,omitempty"`
	Message string `json:"message,omitempty"`

	// send_file fields (file data is base64-encoded)
	FileDataB64  string `json:"file_data_b64,omitempty"`
	Filename     string `json:"filename,omitempty"`
	Caption      string `json:"caption,omitempty"`
	MimeOverride string `json:"mime_override,omitempty"`

	// mark_read fields
	Chat       string   `json:"chat,omitempty"`
	MessageIDs []string `json:"message_ids,omitempty"`
}

// SocketResponse is a JSON response sent over the Unix domain socket.
type SocketResponse struct {
	OK    bool              `json:"ok"`
	Error string            `json:"error,omitempty"`
	ID    string            `json:"id,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
}

// socketServer wraps a Unix domain socket listener for IPC.
type socketServer struct {
	path     string
	listener net.Listener
	handler  func(SocketRequest) SocketResponse
	wg       sync.WaitGroup
	done     chan struct{}
}

// SocketPath returns the path to the Unix domain socket for a store directory.
func SocketPath(storeDir string) string {
	return filepath.Join(storeDir, "wacli.sock")
}

// StartSocketServer creates and starts a Unix domain socket server.
func (a *App) StartSocketServer(ctx context.Context, handler func(SocketRequest) SocketResponse) (*socketServer, error) {
	sockPath := SocketPath(a.opts.StoreDir)

	// Remove any stale socket file.
	_ = os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen on socket %s: %w", sockPath, err)
	}

	// Make socket accessible only to the owner.
	_ = os.Chmod(sockPath, 0600)

	srv := &socketServer{
		path:     sockPath,
		listener: listener,
		handler:  handler,
		done:     make(chan struct{}),
	}

	// Accept connections in background.
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-srv.done:
					return
				default:
					// Transient error, keep going.
					continue
				}
			}
			srv.wg.Add(1)
			go func() {
				defer srv.wg.Done()
				srv.handleConn(conn)
			}()
		}
	}()

	// Cleanup when context is cancelled.
	go func() {
		select {
		case <-ctx.Done():
			srv.Stop()
		case <-srv.done:
		}
	}()

	return srv, nil
}

func (srv *socketServer) handleConn(conn net.Conn) {
	defer conn.Close()
	// Set a generous deadline for the entire interaction.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024*1024), 64*1024*1024) // 64MB max for file transfers
	if scanner.Scan() {
		line := scanner.Text()
		var req SocketRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			resp := SocketResponse{OK: false, Error: fmt.Sprintf("invalid request: %v", err)}
			data, _ := json.Marshal(resp)
			_, _ = conn.Write(append(data, '\n'))
			return
		}

		resp := srv.handler(req)
		data, _ := json.Marshal(resp)
		_, _ = conn.Write(append(data, '\n'))
	}
}

// Stop shuts down the socket server and removes the socket file.
func (srv *socketServer) Stop() {
	select {
	case <-srv.done:
		return // already stopped
	default:
		close(srv.done)
	}
	_ = srv.listener.Close()
	srv.wg.Wait()
	_ = os.Remove(srv.path)
}

// SendSocketRequest connects to a running sync process via Unix socket and sends a request.
func SendSocketRequest(storeDir string, req SocketRequest) (SocketResponse, error) {
	sockPath := SocketPath(storeDir)

	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return SocketResponse{}, fmt.Errorf("connect to socket: %w", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	data, err := json.Marshal(req)
	if err != nil {
		return SocketResponse{}, fmt.Errorf("marshal request: %w", err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return SocketResponse{}, fmt.Errorf("write request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return SocketResponse{}, fmt.Errorf("read response: %w", err)
		}
		return SocketResponse{}, fmt.Errorf("empty response from socket")
	}

	var resp SocketResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return SocketResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return resp, nil
}

// IsSocketAvailable checks if a sync process is running and responsive.
func IsSocketAvailable(storeDir string) bool {
	resp, err := SendSocketRequest(storeDir, SocketRequest{Action: "ping"})
	if err != nil {
		return false
	}
	return resp.OK
}

// makeSocketHandler creates a handler function for the socket server that
// processes send and read requests using the app's WA client.
func (a *App) makeSocketHandler(ctx context.Context) func(SocketRequest) SocketResponse {
	return func(req SocketRequest) SocketResponse {
		switch req.Action {
		case "ping":
			return SocketResponse{OK: true}
		case "send_text":
			return a.handleSocketSendText(ctx, req)
		case "send_file":
			return a.handleSocketSendFile(ctx, req)
		case "mark_read":
			return a.handleSocketMarkRead(ctx, req)
		default:
			return SocketResponse{OK: false, Error: fmt.Sprintf("unknown action: %s", req.Action)}
		}
	}
}

func (a *App) handleSocketSendText(ctx context.Context, req SocketRequest) SocketResponse {
	if req.To == "" || req.Message == "" {
		return SocketResponse{OK: false, Error: "--to and --message are required"}
	}

	toJID, err := parseJID(req.To)
	if err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("invalid JID: %v", err)}
	}

	msgID, err := a.wa.SendText(ctx, toJID, req.Message)
	if err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("send failed: %v", err)}
	}

	// Store in local DB.
	now := time.Now().UTC()
	chatName := a.wa.ResolveChatName(ctx, toJID, "")
	kind := chatKind(toJID)
	_ = a.db.UpsertChat(toJID.String(), kind, chatName, now)
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:    toJID.String(),
		ChatName:   chatName,
		MsgID:      string(msgID),
		SenderJID:  "",
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       req.Message,
	})

	return SocketResponse{OK: true, ID: string(msgID)}
}

func (a *App) handleSocketSendFile(ctx context.Context, req SocketRequest) SocketResponse {
	if req.To == "" || req.FileDataB64 == "" {
		return SocketResponse{OK: false, Error: "--to and file data are required"}
	}

	toJID, err := parseJID(req.To)
	if err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("invalid JID: %v", err)}
	}

	fileData, err := DecodeBase64(req.FileDataB64)
	if err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("decode file data: %v", err)}
	}

	// Write to a temp file so sendFile can work with it.
	tmpFile, err := os.CreateTemp("", "wacli-send-*")
	if err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("create temp file: %v", err)}
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(fileData); err != nil {
		tmpFile.Close()
		return SocketResponse{OK: false, Error: fmt.Sprintf("write temp file: %v", err)}
	}
	tmpFile.Close()

	// Use the same sendFile logic (we need to duplicate part of the logic here
	// since we can't import cmd package from internal).
	msgID, meta, err := a.SendFileFromData(ctx, toJID, fileData, req.Filename, req.Caption, req.MimeOverride)
	if err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("send file failed: %v", err)}
	}

	return SocketResponse{OK: true, ID: msgID, Meta: meta}
}

func (a *App) handleSocketMarkRead(ctx context.Context, req SocketRequest) SocketResponse {
	if req.Chat == "" || len(req.MessageIDs) == 0 {
		return SocketResponse{OK: false, Error: "--chat and --message are required"}
	}

	chatJID, err := parseJID(req.Chat)
	if err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("invalid chat JID: %v", err)}
	}

	msgIDs := make([]types.MessageID, len(req.MessageIDs))
	for i, id := range req.MessageIDs {
		msgIDs[i] = types.MessageID(id)
	}

	if err := a.wa.MarkRead(ctx, chatJID, msgIDs); err != nil {
		return SocketResponse{OK: false, Error: fmt.Sprintf("mark read failed: %v", err)}
	}

	return SocketResponse{OK: true}
}

func parseJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, fmt.Errorf("JID is required")
	}
	if strings.Contains(s, "@") {
		return types.ParseJID(s)
	}
	return types.JID{User: s, Server: types.DefaultUserServer}, nil
}

// EncodeFileToBase64 reads a file and returns its base64-encoded content.
func EncodeFileToBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// DecodeBase64 decodes a base64-encoded string.
func DecodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimSpace(s))
}
