// Package ipc provides Unix socket IPC for wacli sync/send coordination.
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	socketName    = "wacli.sock"
	readTimeout   = 30 * time.Second
	writeTimeout  = 30 * time.Second
)

// Request represents a command sent to the sync daemon.
type Request struct {
	Command     string `json:"command"` // "send_text", "send_file", "delete_message", "ping"
	To          string `json:"to,omitempty"`
	Message     string `json:"message,omitempty"`
	File        string `json:"file,omitempty"`
	Caption     string `json:"caption,omitempty"`
	Chat        string `json:"chat,omitempty"`
	MsgID       string `json:"msg_id,omitempty"`
	ForEveryone bool   `json:"for_everyone,omitempty"`
}

// Response represents the result from the sync daemon.
type Response struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// SendTextResult is returned for send_text commands.
type SendTextResult struct {
	To    string `json:"to"`
	MsgID string `json:"msg_id"`
}

// Handler processes incoming IPC requests.
type Handler interface {
	SendText(to, message string) (msgID string, err error)
	DeleteMessage(chat, msgID string, forEveryone bool) error
}

// Server listens on a Unix socket for IPC requests.
type Server struct {
	storeDir string
	handler  Handler
	listener net.Listener
	wg       sync.WaitGroup
	done     chan struct{}
}

// NewServer creates an IPC server.
func NewServer(storeDir string, handler Handler) *Server {
	return &Server{
		storeDir: storeDir,
		handler:  handler,
		done:     make(chan struct{}),
	}
}

// SocketPath returns the path to the Unix socket.
func SocketPath(storeDir string) string {
	return filepath.Join(storeDir, socketName)
}

// Start begins listening for connections.
func (s *Server) Start() error {
	sockPath := SocketPath(s.storeDir)
	
	// Remove stale socket if exists
	_ = os.Remove(sockPath)
	
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}
	s.listener = listener
	
	s.wg.Add(1)
	go s.acceptLoop()
	
	return nil
}

// Stop shuts down the server.
func (s *Server) Stop() {
	close(s.done)
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.wg.Wait()
	_ = os.Remove(SocketPath(s.storeDir))
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		default:
		}
		
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				continue
			}
		}
		
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	
	// Recover from panics in the handler
	defer func() {
		if r := recover(); r != nil {
			s.writeResponse(conn, Response{Success: false, Error: fmt.Sprintf("internal error: %v", r)})
		}
	}()
	
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		s.writeResponse(conn, Response{Success: false, Error: fmt.Sprintf("read error: %v", err)})
		return
	}
	
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeResponse(conn, Response{Success: false, Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	
	resp := s.processRequest(req)
	s.writeResponse(conn, resp)
}

func (s *Server) processRequest(req Request) Response {
	switch req.Command {
	case "ping":
		return Response{Success: true, Data: "pong"}
	
	case "send_text":
		if req.To == "" || req.Message == "" {
			return Response{Success: false, Error: "to and message are required"}
		}
		msgID, err := s.handler.SendText(req.To, req.Message)
		if err != nil {
			return Response{Success: false, Error: err.Error()}
		}
		return Response{Success: true, Data: SendTextResult{To: req.To, MsgID: msgID}}
	
	case "delete_message":
		if req.Chat == "" || req.MsgID == "" {
			return Response{Success: false, Error: "chat and msg_id are required"}
		}
		err := s.handler.DeleteMessage(req.Chat, req.MsgID, req.ForEveryone)
		if err != nil {
			return Response{Success: false, Error: err.Error()}
		}
		return Response{Success: true, Data: map[string]any{
			"deleted":      true,
			"chat":         req.Chat,
			"msg_id":       req.MsgID,
			"for_everyone": req.ForEveryone,
		}}
	
	default:
		return Response{Success: false, Error: fmt.Sprintf("unknown command: %s", req.Command)}
	}
}

func (s *Server) writeResponse(conn net.Conn, resp Response) {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

// Client connects to a running sync daemon.
type Client struct {
	storeDir string
}

// NewClient creates an IPC client.
func NewClient(storeDir string) *Client {
	return &Client{storeDir: storeDir}
}

// IsAvailable checks if the sync daemon socket exists.
func (c *Client) IsAvailable() bool {
	sockPath := SocketPath(c.storeDir)
	_, err := os.Stat(sockPath)
	return err == nil
}

// SendText sends a text message via the sync daemon.
func (c *Client) SendText(to, message string) (*SendTextResult, error) {
	req := Request{
		Command: "send_text",
		To:      to,
		Message: message,
	}
	
	resp, err := c.send(req)
	if err != nil {
		return nil, err
	}
	
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	
	// Parse the result
	data, _ := json.Marshal(resp.Data)
	var result SendTextResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	
	return &result, nil
}

// Ping checks if the daemon is responsive.
func (c *Client) Ping() error {
	req := Request{Command: "ping"}
	resp, err := c.send(req)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// DeleteMessage deletes a message via the sync daemon.
func (c *Client) DeleteMessage(chat, msgID string, forEveryone bool) error {
	req := Request{
		Command:     "delete_message",
		Chat:        chat,
		MsgID:       msgID,
		ForEveryone: forEveryone,
	}
	
	resp, err := c.send(req)
	if err != nil {
		return err
	}
	
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	
	return nil
}

func (c *Client) send(req Request) (*Response, error) {
	sockPath := SocketPath(c.storeDir)
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()
	
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	
	return &resp, nil
}
