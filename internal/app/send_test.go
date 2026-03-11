package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow"
)

func TestSendText_ParsesPhoneConnectsAndPersists(t *testing.T) {
	a := newTestApp(t)
	fw := newFakeWA()
	a.wa = fw

	res, err := a.SendText(context.Background(), SendTextParams{
		To:      "6591234567",
		Message: "hello world",
	})
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}

	// Verify result
	wantJID := "6591234567@s.whatsapp.net"
	if res.To != wantJID {
		t.Errorf("To = %q, want %q", res.To, wantJID)
	}
	if res.ID != "msgid" {
		t.Errorf("ID = %q, want %q", res.ID, "msgid")
	}
	if res.File != nil {
		t.Error("File should be nil for text send")
	}

	// Verify WA was called
	if len(fw.sendTextCalls) != 1 {
		t.Fatalf("expected 1 SendText call, got %d", len(fw.sendTextCalls))
	}
	if fw.sendTextCalls[0].To.String() != wantJID {
		t.Errorf("SendText To = %q, want %q", fw.sendTextCalls[0].To.String(), wantJID)
	}
	if fw.sendTextCalls[0].Text != "hello world" {
		t.Errorf("SendText Text = %q, want %q", fw.sendTextCalls[0].Text, "hello world")
	}

	// Verify DB persistence
	msgs, err := a.db.ListMessages(store.ListMessagesParams{
		ChatJID: wantJID,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !msgs[0].FromMe {
		t.Error("expected FromMe=true")
	}
	if msgs[0].Text != "hello world" {
		t.Errorf("Text = %q, want %q", msgs[0].Text, "hello world")
	}
	if msgs[0].MediaType != "" {
		t.Errorf("MediaType = %q, want empty", msgs[0].MediaType)
	}

	// Verify chat row
	chats, err := a.db.ListChats(wantJID, 10)
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chats))
	}
	if chats[0].Kind != "dm" {
		t.Errorf("chat Kind = %q, want %q", chats[0].Kind, "dm")
	}
}

func TestSendFile_ClassificationAndPersistence(t *testing.T) {
	tests := []struct {
		name         string
		filePath     string
		fileContent  []byte
		filename     string
		mimeOverride string
		wantName     string
		wantMIME     string
		wantMedia    string
		wantUpload   whatsmeow.MediaType
	}{
		{
			name:       "image by extension",
			filePath:   "photo.png",
			fileContent: []byte("fake png data"),
			wantName:   "photo.png",
			wantMIME:   "image/png",
			wantMedia:  "image",
			wantUpload: whatsmeow.MediaImage,
		},
		{
			name:         "video by override",
			filePath:     "file.bin",
			fileContent:  []byte("fake video data"),
			mimeOverride: "video/mp4",
			wantName:     "file.bin",
			wantMIME:     "video/mp4",
			wantMedia:    "video",
			wantUpload:   whatsmeow.MediaVideo,
		},
		{
			name:         "audio by override",
			filePath:     "voice.dat",
			fileContent:  []byte("fake audio data"),
			mimeOverride: "audio/ogg",
			wantName:     "voice.dat",
			wantMIME:     "audio/ogg",
			wantMedia:    "audio",
			wantUpload:   whatsmeow.MediaAudio,
		},
		{
			name:        "document by sniff",
			filePath:    "data.unknownext",
			fileContent: []byte("just some plain text data for sniffing"),
			wantName:    "data.unknownext",
			wantMIME:    "text/plain; charset=utf-8",
			wantMedia:   "document",
			wantUpload:  whatsmeow.MediaDocument,
		},
		{
			name:        "filename override does not affect MIME",
			filePath:    "report.pdf",
			fileContent: []byte("fake pdf data"),
			filename:    "renamed.png",
			wantName:    "renamed.png",
			wantMIME:    "application/pdf",
			wantMedia:   "document",
			wantUpload:  whatsmeow.MediaDocument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApp(t)
			fw := newFakeWA()
			a.wa = fw

			// Write test file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, tt.filePath)
			if err := os.WriteFile(tmpFile, tt.fileContent, 0600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			res, err := a.SendFile(context.Background(), SendFileParams{
				To:       "6591234567",
				FilePath: tmpFile,
				Filename: tt.filename,
				Caption:  "test caption",
				MIMEType: tt.mimeOverride,
			})
			if err != nil {
				t.Fatalf("SendFile: %v", err)
			}

			// Verify result metadata
			if res.File == nil {
				t.Fatal("File should not be nil")
			}
			if res.File.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", res.File.Name, tt.wantName)
			}
			if res.File.MIMEType != tt.wantMIME {
				t.Errorf("MIMEType = %q, want %q", res.File.MIMEType, tt.wantMIME)
			}
			if res.File.Media != tt.wantMedia {
				t.Errorf("Media = %q, want %q", res.File.Media, tt.wantMedia)
			}

			// Verify upload media type
			if len(fw.uploadCalls) != 1 {
				t.Fatalf("expected 1 Upload call, got %d", len(fw.uploadCalls))
			}
			if fw.uploadCalls[0].MediaType != tt.wantUpload {
				t.Errorf("Upload MediaType = %v, want %v", fw.uploadCalls[0].MediaType, tt.wantUpload)
			}

			// Verify proto was built (SendProtoMessage called)
			if len(fw.sendProtoCalls) != 1 {
				t.Fatalf("expected 1 SendProtoMessage call, got %d", len(fw.sendProtoCalls))
			}

			// Verify DB persistence
			msgs, err := a.db.ListMessages(store.ListMessagesParams{
				ChatJID: "6591234567@s.whatsapp.net",
				Limit:   10,
			})
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}
			if len(msgs) != 1 {
				t.Fatalf("expected 1 message, got %d", len(msgs))
			}
			if !msgs[0].FromMe {
				t.Error("expected FromMe=true")
			}
			if msgs[0].MediaType != tt.wantMedia {
				t.Errorf("persisted MediaType = %q, want %q", msgs[0].MediaType, tt.wantMedia)
			}
		})
	}
}

func TestSendText_BestEffortPersistence(t *testing.T) {
	a := newTestApp(t)
	fw := newFakeWA()
	a.wa = fw

	// Close DB to force persistence errors
	_ = a.db.Close()

	res, err := a.SendText(context.Background(), SendTextParams{
		To:      "6591234567",
		Message: "hello",
	})
	if err != nil {
		t.Fatalf("SendText should succeed even with closed DB: %v", err)
	}
	if res.ID != "msgid" {
		t.Errorf("ID = %q, want %q", res.ID, "msgid")
	}
	// WA send should still have been called
	if len(fw.sendTextCalls) != 1 {
		t.Fatalf("expected 1 SendText call, got %d", len(fw.sendTextCalls))
	}
}

func TestSendFile_BestEffortPersistence(t *testing.T) {
	a := newTestApp(t)
	fw := newFakeWA()
	a.wa = fw

	// Write a test file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(tmpFile, []byte("content"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Close DB to force persistence errors
	_ = a.db.Close()

	res, err := a.SendFile(context.Background(), SendFileParams{
		To:       "6591234567",
		FilePath: tmpFile,
	})
	if err != nil {
		t.Fatalf("SendFile should succeed even with closed DB: %v", err)
	}
	if res.ID != "msgid" {
		t.Errorf("ID = %q, want %q", res.ID, "msgid")
	}
	if len(fw.sendProtoCalls) != 1 {
		t.Fatalf("expected 1 SendProtoMessage call, got %d", len(fw.sendProtoCalls))
	}
}
