package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// fakeSendFileWA stubs only the methods sendFile touches; any other call
// panics through the embedded nil interface, catching accidental coupling.
type fakeSendFileWA struct {
	app.WAClient
	sentID     types.MessageID
	mappedChat types.JID
	wireTarget types.JID
	sent       *waProto.Message
}

func (f *fakeSendFileWA) Upload(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	// Preserve real content identity across the fake upload/store boundary.
	hash := sha256.Sum256(data)
	return whatsmeow.UploadResponse{FileSHA256: hash[:], FileLength: uint64(len(data))}, nil
}

func (f *fakeSendFileWA) SendProtoMessage(_ context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	f.wireTarget = to
	f.sent = msg
	return f.sentID, nil
}

func (f *fakeSendFileWA) ResolveChatName(context.Context, types.JID, string) string {
	return "Test Chat"
}

func (f *fakeSendFileWA) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	if !f.mappedChat.IsEmpty() {
		return f.mappedChat
	}
	return jid
}

// Quoted replies look the target up under the chat's phone/LID alias and read
// the linked account; model an unmapped phone and an unknown account.
func (f *fakeSendFileWA) ResolvePNToLID(_ context.Context, jid types.JID) types.JID {
	return jid
}

func (f *fakeSendFileWA) LinkedJID() string {
	return ""
}

type fakeSendFileApp struct {
	wa app.WAClient
	db *store.DB
}

func (f *fakeSendFileApp) WA() app.WAClient { return f.wa }
func (f *fakeSendFileApp) DB() *store.DB    { return f.db }

func newSendFileFixture(t *testing.T) (*fakeSendFileApp, *store.DB, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	filePath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	a := &fakeSendFileApp{wa: &fakeSendFileWA{sentID: "MSG1"}, db: db}
	return a, db, filePath
}

func TestSendFilePersistsHistory(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendFile(context.Background(), a, to, filePath, sendFileOptions{caption: "hi"})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if res.id != "MSG1" || res.storeWarning != nil {
		t.Fatalf("expected clean outcome, got %+v", res)
	}
	msg, err := db.GetMessage(to.String(), "MSG1")
	if err != nil {
		t.Fatalf("sent message not persisted: %v", err)
	}
	if !msg.FromMe || msg.Text != "hi" {
		t.Fatalf("unexpected persisted message: %+v", msg)
	}
}

func TestSendFilePersistsCanonicalRecipient(t *testing.T) {
	phone := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("20001", types.HiddenUserServer)
	for _, tc := range []struct {
		name               string
		to, mapped, stored types.JID
	}{
		{name: "mapped LID", to: lid, mapped: phone, stored: phone},
		{name: "unknown LID", to: lid, stored: lid},
		{name: "phone", to: phone, stored: phone},
		{name: "group", to: types.NewJID("123-456", types.GroupServer), stored: types.NewJID("123-456", types.GroupServer)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, db, filePath := newSendFileFixture(t)
			fake := a.wa.(*fakeSendFileWA)
			fake.mappedChat = tc.mapped
			res, err := sendFile(context.Background(), a, tc.to, filePath, sendFileOptions{caption: "caption"})
			if err != nil || res.storeWarning != nil || res.id != "MSG1" {
				t.Fatalf("accepted outcome = %+v, err %v", res, err)
			}
			if fake.wireTarget != tc.to {
				t.Fatalf("wire target = %s, want %s", fake.wireTarget, tc.to)
			}
			message, err := db.GetMessage(tc.stored.String(), res.id)
			if err != nil || message.MediaCaption != "caption" || !message.FromMe {
				t.Fatalf("exact canonical attachment history = %+v, err %v", message, err)
			}
			if _, err := db.GetChat(tc.stored.String()); err != nil {
				t.Fatalf("canonical chat missing: %v", err)
			}
			media, err := db.GetMediaDownloadInfo(tc.stored.String(), res.id)
			hash := sha256.Sum256([]byte("hello"))
			if err != nil || !bytes.Equal(media.FileSHA256, hash[:]) || media.FileLength != 5 {
				t.Fatalf("accepted attachment identity = %+v, err %v", media, err)
			}
			// A mapped transport alias must not create a sibling message or chat.
			if tc.stored != tc.to {
				if _, err := db.GetMessage(tc.to.String(), res.id); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("transport sibling message lookup = %v", err)
				}
				if _, err := db.GetChat(tc.to.String()); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("transport sibling chat lookup = %v", err)
				}
			}
		})
	}
}

// Regression for the silently-swallowed store failures: a delivered message
// whose local persistence fails must keep the ID and surface a warning, not
// return a hard error that invites callers to retry an already-sent message.
func TestSendFileStoreFailureKeepsDeliveredID(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendFile(context.Background(), a, to, filePath, sendFileOptions{})
	if err != nil {
		t.Fatalf("store failure must not fail the send, got %v", err)
	}
	if res.id != "MSG1" {
		t.Fatalf("delivered ID lost, got %+v", res)
	}
	if res.storeWarning == nil {
		t.Fatal("expected store warning for failed history update")
	}
}

func TestSendFileStatusStoreFailureKeepsDeliveredID(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	res, err := sendFile(context.Background(), a, types.StatusBroadcastJID, filePath, sendFileOptions{})
	if err != nil {
		t.Fatalf("store failure must not fail the send, got %v", err)
	}
	if res.id != "MSG1" || res.storeWarning == nil {
		t.Fatalf("expected delivered ID with store warning, got %+v", res)
	}
}

func TestSendFileStatusPersistsHistory(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)

	res, err := sendFile(context.Background(), a, types.StatusBroadcastJID, filePath, sendFileOptions{caption: "st"})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if res.storeWarning != nil {
		t.Fatalf("unexpected store warning: %v", res.storeWarning)
	}
	if _, err := db.GetStatusMessage("MSG1"); err != nil {
		t.Fatalf("status message not persisted: %v", err)
	}
}

// testVoiceWaveform is a caller-measured waveform: 64 levels rising 0..100.
func testVoiceWaveform() []int {
	levels := make([]int, voiceWaveformSamples)
	for i := range levels {
		levels[i] = i * voiceWaveformMax / (voiceWaveformSamples - 1)
	}
	return levels
}

// installRecordingProbes puts an ffprobe and an ffmpeg that fail on PATH, and
// nothing else, so a test sees which of them a send ran. The returned function
// lists the tools run, in order.
func installRecordingProbes(t *testing.T) func() []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("probe stand-ins are shell scripts")
	}
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "probes")
	for _, tool := range []string{"ffprobe", "ffmpeg"} {
		script := "#!/bin/sh\necho " + tool + " >> \"$PROBE_MARKER\"\necho 'no audio here' >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("PROBE_MARKER", marker)
	return func() []string {
		data, err := os.ReadFile(marker)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatalf("read probe marker: %v", err)
		}
		return strings.Fields(string(data))
	}
}

func newVoiceNoteFixture(t *testing.T) (*fakeSendFileApp, string) {
	t.Helper()
	a, _, _ := newSendFileFixture(t)
	filePath := filepath.Join(t.TempDir(), "note.ogg")
	if err := os.WriteFile(filePath, []byte("OggS stand-in"), 0o600); err != nil {
		t.Fatalf("write voice note: %v", err)
	}
	return a, filePath
}

func TestSendFileUsesGivenVoiceMetadataWithoutProbing(t *testing.T) {
	probes := installRecordingProbes(t)
	a, filePath := newVoiceNoteFixture(t)
	seconds := int64(24)
	voice, err := parseVoiceNoteMetadata(&seconds, testVoiceWaveform(), true)
	if err != nil {
		t.Fatalf("parseVoiceNoteMetadata: %v", err)
	}

	res, err := sendFile(context.Background(), a, types.NewJID("15550000001", types.DefaultUserServer), filePath, sendFileOptions{ptt: true, voice: voice})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if ran := probes(); len(ran) != 0 {
		t.Fatalf("probes ran %v, want none", ran)
	}
	if res.voiceWarning != "" {
		t.Fatalf("voice warning = %q, want none", res.voiceWarning)
	}
	audio := a.wa.(*fakeSendFileWA).sent.GetAudioMessage()
	if audio == nil || !audio.GetPTT() {
		t.Fatalf("sent %v, want a voice note", a.wa.(*fakeSendFileWA).sent)
	}
	if audio.GetSeconds() != 24 {
		t.Fatalf("seconds = %d, want 24", audio.GetSeconds())
	}
	want := make([]byte, voiceWaveformSamples)
	for i, level := range testVoiceWaveform() {
		want[i] = byte(level)
	}
	if !bytes.Equal(audio.GetWaveform(), want) {
		t.Fatalf("waveform = %v, want %v", audio.GetWaveform(), want)
	}
}

func TestSendFileProbesOnlyTheVoiceMetadataLeftOut(t *testing.T) {
	probes := installRecordingProbes(t)
	a, filePath := newVoiceNoteFixture(t)
	seconds := int64(5)
	voice, err := parseVoiceNoteMetadata(&seconds, nil, true)
	if err != nil {
		t.Fatalf("parseVoiceNoteMetadata: %v", err)
	}

	res, err := sendFile(context.Background(), a, types.NewJID("15550000001", types.DefaultUserServer), filePath, sendFileOptions{ptt: true, voice: voice})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if ran := strings.Join(probes(), " "); ran != "ffmpeg" {
		t.Fatalf("probes ran %q, want ffmpeg alone", ran)
	}
	audio := a.wa.(*fakeSendFileWA).sent.GetAudioMessage()
	if audio.GetSeconds() != 5 || audio.Waveform != nil {
		t.Fatalf("seconds %d waveform %v, want 5 and none", audio.GetSeconds(), audio.GetWaveform())
	}
	if want := "voice note waveform not measured: ffmpeg: exit status 1: no audio here"; res.voiceWarning != want {
		t.Fatalf("voice warning = %q, want %q", res.voiceWarning, want)
	}
}

func TestSendFileWithoutFFmpegSendsTheVoiceNoteBareAndSaysSo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH without probes is set up for Unix")
	}
	t.Setenv("PATH", t.TempDir())
	a, filePath := newVoiceNoteFixture(t)

	res, err := sendFile(context.Background(), a, types.NewJID("15550000001", types.DefaultUserServer), filePath, sendFileOptions{ptt: true})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	audio := a.wa.(*fakeSendFileWA).sent.GetAudioMessage()
	if audio == nil || !audio.GetPTT() || audio.Seconds != nil || audio.Waveform != nil {
		t.Fatalf("sent %v, want a bare voice note", audio)
	}
	want := "voice note length not measured: ffprobe is not installed; voice note waveform not measured: ffmpeg is not installed"
	if res.voiceWarning != want {
		t.Fatalf("voice warning = %q, want %q", res.voiceWarning, want)
	}
}

func TestSendFileRefusesVoiceMetadataOutsideVoiceNoteMode(t *testing.T) {
	a, filePath := newVoiceNoteFixture(t)
	_, err := sendFile(context.Background(), a, types.NewJID("15550000001", types.DefaultUserServer), filePath, sendFileOptions{voice: voiceNoteMetadata{seconds: 3}})
	if err == nil || !strings.Contains(err.Error(), "voice-note mode") {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if a.wa.(*fakeSendFileWA).sent != nil {
		t.Fatal("a refused send went out")
	}
}
