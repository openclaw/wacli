package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
)

func newAgentTestApp(t *testing.T) (*App, *fakeWA) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wacli.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	fw := newFakeWA()
	return &App{
		opts: Options{StoreDir: dir, Version: "0.5.0-test"},
		wa:   fw,
		db:   db,
	}, fw
}

func TestAgentReady(t *testing.T) {
	a, _ := newAgentTestApp(t)

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Empty stdin → agent reads EOF and exits.
	err := a.RunAgent(ctx, strings.NewReader(""), &out, AgentOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Should contain both connection and agent.ready notifications.
	dec := json.NewDecoder(&out)
	found := false
	for {
		var notif rpcNotification
		if err := dec.Decode(&notif); err != nil {
			break
		}
		if notif.Method == "agent.ready" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected agent.ready notification")
	}
}

func TestAgentListChats(t *testing.T) {
	a, _ := newAgentTestApp(t)

	// Seed a chat.
	_ = a.db.UpsertChat("5511999@s.whatsapp.net", "dm", "Alice", time.Now())

	req := `{"jsonrpc":"2.0","id":1,"method":"list_chats","params":{"limit":10}}` + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(req), &out, AgentOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Parse: first line is agent.ready, second line is the response.
	dec := json.NewDecoder(&out)

	// Skip agent.ready
	var notif rpcNotification
	if err := dec.Decode(&notif); err != nil {
		t.Fatal(err)
	}

	// Skip connection notification
	var notif2 rpcNotification
	if err := dec.Decode(&notif2); err != nil {
		t.Fatal(err)
	}

	var resp rpcResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	// Result should be an array with one chat.
	data, _ := json.Marshal(resp.Result)
	var chats []map[string]any
	if err := json.Unmarshal(data, &chats); err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chats))
	}
	if chats[0]["name"] != "Alice" {
		t.Fatalf("expected name Alice, got %v", chats[0]["name"])
	}
}

func TestAgentSendText(t *testing.T) {
	a, _ := newAgentTestApp(t)

	req := `{"jsonrpc":"2.0","id":2,"method":"send_text","params":{"to":"5511999@s.whatsapp.net","text":"hello"}}` + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(req), &out, AgentOptions{AutoPresence: false})
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&out)

	// Skip agent.ready
	var notif rpcNotification
	if err := dec.Decode(&notif); err != nil {
		t.Fatal(err)
	}
	// Skip connection notification
	var notif2 rpcNotification
	if err := dec.Decode(&notif2); err != nil {
		t.Fatal(err)
	}

	var resp rpcResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	data, _ := json.Marshal(resp.Result)
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["message_id"] == nil || result["message_id"] == "" {
		t.Fatal("expected non-empty message_id")
	}
}

func TestAgentMethodNotFound(t *testing.T) {
	a, _ := newAgentTestApp(t)

	req := `{"jsonrpc":"2.0","id":3,"method":"bogus","params":{}}` + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(req), &out, AgentOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&out)

	// Skip agent.ready
	var notif rpcNotification
	if err := dec.Decode(&notif); err != nil {
		t.Fatal(err)
	}
	// Skip connection notification
	var notif2 rpcNotification
	if err := dec.Decode(&notif2); err != nil {
		t.Fatal(err)
	}

	var resp rpcResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != errCodeMethodNotFound {
		t.Fatalf("expected code %d, got %d", errCodeMethodNotFound, resp.Error.Code)
	}
}

func TestAgentSearch(t *testing.T) {
	a, _ := newAgentTestApp(t)

	// Seed a message.
	_ = a.db.UpsertChat("5511999@s.whatsapp.net", "dm", "Alice", time.Now())
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   "5511999@s.whatsapp.net",
		ChatName:  "Alice",
		MsgID:     "msg1",
		Timestamp: time.Now(),
		FromMe:    false,
		Text:      "hello world",
	})

	req := `{"jsonrpc":"2.0","id":4,"method":"search","params":{"query":"hello"}}` + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(req), &out, AgentOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&out)
	// Skip agent.ready
	var notif rpcNotification
	if err := dec.Decode(&notif); err != nil {
		t.Fatal(err)
	}
	// Skip connection notification
	var notif2 rpcNotification
	if err := dec.Decode(&notif2); err != nil {
		t.Fatal(err)
	}

	var resp rpcResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	data, _ := json.Marshal(resp.Result)
	var msgs []map[string]any
	if err := json.Unmarshal(data, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least 1 search result")
	}
}

func TestAgentGetMessage(t *testing.T) {
	a, _ := newAgentTestApp(t)

	_ = a.db.UpsertChat("5511999@s.whatsapp.net", "dm", "Alice", time.Now())
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:   "5511999@s.whatsapp.net",
		ChatName:  "Alice",
		MsgID:     "msg1",
		Timestamp: time.Now(),
		FromMe:    false,
		Text:      "test message",
	})

	req := `{"jsonrpc":"2.0","id":5,"method":"get_message","params":{"chat":"5511999@s.whatsapp.net","id":"msg1"}}` + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(req), &out, AgentOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&out)
	// Skip agent.ready
	var notif rpcNotification
	if err := dec.Decode(&notif); err != nil {
		t.Fatal(err)
	}
	// Skip connection notification
	var notif2 rpcNotification
	if err := dec.Decode(&notif2); err != nil {
		t.Fatal(err)
	}

	var resp rpcResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	data, _ := json.Marshal(resp.Result)
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg["text"] != "test message" {
		t.Fatalf("expected text 'test message', got %v", msg["text"])
	}
}

func TestAgentSendFile(t *testing.T) {
	a, _ := newAgentTestApp(t)

	// Create a temp file to send.
	tmpFile := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(tmpFile, []byte("file content"), 0600); err != nil {
		t.Fatal(err)
	}

	req := `{"jsonrpc":"2.0","id":6,"method":"send_file","params":{"to":"5511999@s.whatsapp.net","file":"` + tmpFile + `"}}` + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(req), &out, AgentOptions{AutoPresence: false})
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&out)
	// Skip agent.ready
	var notif rpcNotification
	if err := dec.Decode(&notif); err != nil {
		t.Fatal(err)
	}
	// Skip connection notification
	var notif2 rpcNotification
	if err := dec.Decode(&notif2); err != nil {
		t.Fatal(err)
	}

	var resp rpcResponse
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	data, _ := json.Marshal(resp.Result)
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["message_id"] == nil || result["message_id"] == "" {
		t.Fatal("expected non-empty message_id")
	}
}

func TestAgentMultipleRequests(t *testing.T) {
	a, _ := newAgentTestApp(t)

	_ = a.db.UpsertChat("5511999@s.whatsapp.net", "dm", "Alice", time.Now())

	requests := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"list_chats","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"send_text","params":{"to":"5511999@s.whatsapp.net","text":"hi"}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(requests), &out, AgentOptions{AutoPresence: false})
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&out)

	// Skip agent.ready and connection
	for i := 0; i < 2; i++ {
		var n rpcNotification
		if err := dec.Decode(&n); err != nil {
			t.Fatal(err)
		}
	}

	// Two responses.
	for i := 0; i < 2; i++ {
		var resp rpcResponse
		if err := dec.Decode(&resp); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if resp.Error != nil {
			t.Fatalf("request %d: unexpected error: %s", i+1, resp.Error.Message)
		}
	}
}

func TestAgentParseError(t *testing.T) {
	a, _ := newAgentTestApp(t)

	// Send invalid JSON then valid request.
	input := "not json\n" +
		`{"jsonrpc":"2.0","id":1,"method":"list_chats","params":{}}` + "\n"

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := a.RunAgent(ctx, strings.NewReader(input), &out, AgentOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Collect all output messages.
	dec := json.NewDecoder(&out)
	var foundParseError, foundListChats bool
	for {
		// Try to decode as a generic JSON object.
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		// Try as response (has id field).
		var resp rpcResponse
		if err := json.Unmarshal(raw, &resp); err == nil && resp.JSONRPC == "2.0" {
			if resp.Error != nil && resp.Error.Code == errCodeParse {
				foundParseError = true
			}
			if resp.Error == nil && resp.ID != nil {
				foundListChats = true
			}
		}
	}
	if !foundParseError {
		t.Fatal("expected parse error response")
	}
	if !foundListChats {
		t.Fatal("expected successful list_chats response after parse error recovery")
	}
}
