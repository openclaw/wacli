package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

// Exercise the public command output against a disposable archive. Display
// previews may be normalized; JSON raw fields must retain the stored payload.
func TestMessagesExactHistoryJSON(t *testing.T) {
	storeDir := t.TempDir()
	db, err := store.Open(filepath.Join(storeDir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	chat := "15550000001@s.whatsapp.net"
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	wantText, wantCaption := " \t needle original\r\nbody α \n", "\n needle caption β \t"
	edited := " \t needle edited\r\nbody γ \n"
	if err := db.UpsertChat(chat, "dm", "Fixture", now); err != nil {
		t.Fatal(err)
	}
	for _, row := range []store.UpsertMessageParams{
		{ChatJID: chat, MsgID: "original", Timestamp: now, Text: wantText, MediaCaption: wantCaption},
		{ChatJID: chat, MsgID: "edited", Timestamp: now.Add(time.Second), Text: "old"},
	} {
		if err := db.UpsertMessage(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpdateMessageText(chat, "edited", edited); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"show", "list", "search", "export"} {
		t.Run(command, func(t *testing.T) {
			args := []string{"--store", storeDir, "--read-only", "--json", "messages", command, "--chat", chat}
			switch command {
			case "show":
				args = append(args, "--id", "original")
			case "search":
				args = append(args, "needle")
			}
			output := filepath.Join(storeDir, "export.json")
			if command == "export" {
				args = append(args, "--output", output)
			}
			raw := captureRootStdout(t, func() {
				if err := execute(args); err != nil {
					t.Fatalf("%s: %v", command, err)
				}
			})
			if command == "export" {
				bytes, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				raw = string(bytes)
			}
			var envelope struct {
				Success bool            `json:"success"`
				Data    json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
				t.Fatalf("decode %s: %v", command, err)
			}
			if !envelope.Success {
				t.Fatalf("%s failed: %s", command, raw)
			}
			var messages []store.Message
			if command == "show" {
				var message store.Message
				if err := json.Unmarshal(envelope.Data, &message); err != nil {
					t.Fatal(err)
				}
				messages = []store.Message{message}
			} else {
				var page struct {
					Messages []store.Message `json:"messages"`
				}
				if err := json.Unmarshal(envelope.Data, &page); err != nil {
					t.Fatal(err)
				}
				messages = page.Messages
			}
			expectedCount := 2
			if command == "show" {
				expectedCount = 1
			}
			if len(messages) != expectedCount {
				t.Fatalf("%s returned %d messages, want %d", command, len(messages), expectedCount)
			}
			for _, message := range messages {
				switch message.MsgID {
				case "original":
					if message.Text != wantText || message.MediaCaption != wantCaption {
						t.Errorf("%s changed original: text=%q caption=%q", command, message.Text, message.MediaCaption)
					}
				case "edited":
					if message.Text != edited {
						t.Errorf("%s changed edit: text=%q", command, message.Text)
					}
				default:
					t.Errorf("unexpected message %q", message.MsgID)
				}
			}
		})
	}
}
