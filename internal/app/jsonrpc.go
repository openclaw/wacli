package app

import (
	"encoding/json"
	"io"
	"sync"
)

// JSON-RPC 2.0 types.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes.
const (
	errCodeParse          = -32700
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeNotConnected   = -32000
	errCodeNotAuthed      = -32001
	errCodeSendFailed     = -32002
)

// rpcWriter is a mutex-protected JSON encoder for writing to stdout.
type rpcWriter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func newRPCWriter(w io.Writer) *rpcWriter {
	return &rpcWriter{enc: json.NewEncoder(w)}
}

func (w *rpcWriter) respond(id json.RawMessage, result any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.enc.Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (w *rpcWriter) respondError(id json.RawMessage, code int, msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.enc.Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	})
}

func (w *rpcWriter) notify(method string, params any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.enc.Encode(rpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}
