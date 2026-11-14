package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

type keepUnreadWA struct {
	*fakeSendFileWA
	sends atomic.Int32
}

func (w *keepUnreadWA) SendText(context.Context, types.JID, string) (types.MessageID, error) {
	w.sends.Add(1)
	return "sent", nil
}

func (w *keepUnreadWA) SendProtoMessage(context.Context, types.JID, *waProto.Message) (types.MessageID, error) {
	w.sends.Add(1)
	return "sent", nil
}

func (w *keepUnreadWA) SendPoll(context.Context, types.JID, string, []string, int, bool) (types.MessageID, error) {
	w.sends.Add(1)
	return "sent", nil
}

func (w *keepUnreadWA) SendReaction(context.Context, types.JID, types.JID, types.MessageID, string) (types.MessageID, error) {
	w.sends.Add(1)
	return "sent", nil
}

func (*keepUnreadWA) LinkedLID() string { return "" }
func (*keepUnreadWA) GetUserInfo(context.Context, []types.JID) (map[types.JID]types.UserInfo, error) {
	return nil, nil
}
func (*keepUnreadWA) IsOnWhatsApp(context.Context, []string) ([]types.IsOnWhatsAppResponse, error) {
	return nil, nil
}

type keepUnreadApp struct {
	*app.App
	client *keepUnreadWA
}

func (a *keepUnreadApp) WA() app.WAClient                                { return a.client }
func (*keepUnreadApp) EnsureAuthed(context.Context) error                { return nil }
func (*keepUnreadApp) Connect(context.Context, bool, func(string)) error { return nil }
func (a *keepUnreadApp) LocalResolver() (app.LocalResolver, error)       { return a.client, nil }

type keepUnreadSendCase struct {
	name    string
	newCmd  func(*rootFlags, outboundSendAppOpener) *cobra.Command
	execute func(context.Context, outboundSendApp, sendDelegateRequest) (sendDelegateResponse, error)
	args    []string
}

func keepUnreadSendCases(t *testing.T, file string) []keepUnreadSendCase {
	t.Helper()
	return []keepUnreadSendCase{
		{"text", newSendTextCmdWithApp, executeDelegatedText, []string{"--message", "automatic reply", "--no-preview"}},
		{"file", newSendFileCmdWithApp, executeDelegatedFile, []string{"--file", file}},
		{"file_ptt", newSendFileCmdWithApp, executeDelegatedFile, []string{"--file", file, "--mime", "audio/ogg", "--ptt"}},
		{"voice", newSendVoiceCmdWithApp, executeDelegatedFile, []string{"--file", file, "--mime", "audio/ogg"}},
		{"sticker", newSendStickerCmdWithApp, executeDelegatedSticker, []string{"--file", minimalStickerWebP(t)}},
		{"location", newSendLocationCmdWithApp, executeDelegatedLocation, []string{"--latitude", "1", "--longitude", "2"}},
		{"poll", newSendPollCmdWithApp, executeDelegatedPoll, []string{"--question", "Ready?", "--option", "Yes", "--option", "No"}},
		{"select", newSendSelectCmdWithApp, executeDelegatedButtonListSelect, []string{"--id", "incoming", "--label", "Yes"}},
		{"select_list", newSendSelectCmdWithApp, executeDelegatedButtonListSelect, []string{"--id", "incoming", "--label", "Yes"}},
		{"react", newSendReactCmdWithApp, executeDelegatedReact, []string{"--id", "incoming"}},
	}
}

func assertSendUnread(t *testing.T, db *store.DB, chat string, unread bool, count int) {
	t.Helper()
	c, err := db.GetChat(chat)
	if err != nil || c.Unread != unread || c.UnreadCount != count {
		t.Fatalf("unread=(%v, %d), want (%v, %d); error=%v", c.Unread, c.UnreadCount, unread, count, err)
	}
}

// Run real Cobra commands, their direct send/persist paths, and their socket
// request producers plus real delegated executors. The only fake is WhatsApp.
func TestSendKeepUnreadExecutionPaths(t *testing.T) {
	// Voice metadata is optional; do not invoke host ffmpeg/ffprobe in fixtures.
	t.Setenv("PATH", t.TempDir())
	_, _, file := newSendFileFixture(t)
	for _, tc := range keepUnreadSendCases(t, file) {
		for _, delegated := range []bool{false, true} {
			for _, keep := range []bool{false, true} {
				for _, count := range []int{0, 2} {
					t.Run(fmt.Sprintf("%s/delegated=%v/keep=%v/count=%d", tc.name, delegated, keep, count), func(t *testing.T) {
						real, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(real.Close)
						client := &keepUnreadWA{fakeSendFileWA: &fakeSendFileWA{sentID: "sent"}}
						a := &keepUnreadApp{App: real, client: client}
						chat := "15550000001@s.whatsapp.net"
						db := a.DB()
						if err := db.UpsertChat(chat, "dm", "Fixture", time.Now().Add(-time.Minute)); err != nil {
							t.Fatal(err)
						}
						button := store.Button{Type: "quick_reply", ID: "yes", DisplayText: "Yes", ResponseType: selectResponseButtons}
						if tc.name == "select_list" {
							button.Type, button.ResponseType = "list_row", selectResponseList
						}
						if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "incoming", Timestamp: time.Now().Add(-time.Minute), Text: "Please answer", Buttons: []store.Button{button}}); err != nil {
							t.Fatal(err)
						}
						if err := db.SetChatUnreadCount(chat, count); err != nil {
							t.Fatal(err)
						}
						if err := db.SetChatUnread(chat, true); err != nil {
							t.Fatal(err)
						}
						flags := &rootFlags{storeDir: a.StoreDir(), asJSON: true, timeout: 5 * time.Second}
						open := func(context.Context) (outboundSendApp, func(), error) { return a, func() {}, nil }
						if delegated {
							stop, err := startSendDelegateServerForStore(t.Context(), a.StoreDir(), sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
								wantVersion := sendDelegateVersion
								if keep {
									wantVersion = sendKeepUnreadDelegateVersion
								}
								if req.KeepUnread != keep || req.Version != wantVersion {
									return sendDelegateResponse{}, fmt.Errorf("lost policy/version: %+v", req)
								}
								return tc.execute(ctx, a, req)
							})
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(stop)
							open = func(context.Context) (outboundSendApp, func(), error) { return nil, nil, lock.ErrLocked }
						}
						cmd := tc.newCmd(flags, open)
						args := append([]string{"--to", chat, "--post-send-wait", "0"}, tc.args...)
						if keep {
							args = append(args, "--keep-unread")
						}
						cmd.SetArgs(args)
						captureRootStdout(t, func() {
							if err := cmd.Execute(); err != nil {
								t.Fatal(err)
							}
						})
						if client.sends.Load() != 1 {
							t.Fatalf("sends=%d, want exactly one", client.sends.Load())
						}
						wantUnread, wantCount := keep || tc.name == "react", count
						if !wantUnread {
							wantCount = 0
						}
						assertSendUnread(t, db, chat, wantUnread, wantCount)
						stored, err := db.GetMessage(chat, "sent")
						if err != nil || !stored.FromMe {
							t.Fatalf("accepted send missing: %+v, %v", stored, err)
						}
						// Echo and replay do not carry keep_unread. The already-stored
						// own row is not fresh, so neither can acknowledge the chat.
						for range 2 {
							if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: stored.MsgID, Timestamp: stored.Timestamp, FromMe: true, Text: stored.Text, DisplayText: stored.DisplayText, MediaType: stored.MediaType, ReactionToID: stored.ReactionToID, ReactionEmoji: stored.ReactionEmoji}); err != nil {
								t.Fatal(err)
							}
							assertSendUnread(t, db, chat, wantUnread, wantCount)
						}
					})
				}
			}
		}
	}
}

func TestSendKeepUnreadHelp(t *testing.T) {
	for _, sub := range newSendCmd(&rootFlags{}).Commands() {
		t.Run(sub.Name(), func(t *testing.T) {
			var output bytes.Buffer
			sub.SetOut(&output)
			if err := sub.Help(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "--keep-unread") {
				t.Fatalf("flag missing from help: %s", output.String())
			}
		})
	}
}

func TestSendKeepUnreadJSON(t *testing.T) {
	for _, keep := range []bool{false, true} {
		raw, err := json.Marshal(sendDelegateRequest{Kind: "text", KeepUnread: keep})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"keep_unread":true`) != keep || strings.Contains(string(raw), `"keep_unread":false`) {
			t.Fatalf("keep_unread must be true or omitted: %s", raw)
		}
		var decoded sendDelegateRequest
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded.KeepUnread != keep {
			t.Fatalf("round trip: %+v, %v", decoded, err)
		}
	}
}

func TestSendKeepUnreadDelegateVersionAccepted(t *testing.T) {
	// Invalid ephemeral options stop before app use, but after version dispatch.
	_, err := executeDelegatedSend(t.Context(), nil, sendDelegateRequest{Version: sendKeepUnreadDelegateVersion, Kind: "text", KeepUnread: true, EphemeralDurationSet: true})
	if err == nil || !strings.Contains(err.Error(), "--ephemeral-duration") {
		t.Fatalf("v2 keep-unread request did not reach the text executor: %v", err)
	}
}

func TestSendKeepUnreadStickerCanonicalEcho(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(fmt.Sprintf("delegated=%v", delegated), func(t *testing.T) {
			real, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(real.Close)
			phone := types.NewJID("15550000001", types.DefaultUserServer)
			lid := types.NewJID("20001", types.HiddenUserServer)
			client := &keepUnreadWA{fakeSendFileWA: &fakeSendFileWA{sentID: "sent", mappedChat: phone}}
			a := &keepUnreadApp{App: real, client: client}
			db := a.DB()
			if err := db.UpsertChat(phone.String(), "dm", "Fixture", time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := db.SetChatUnreadCount(phone.String(), 2); err != nil {
				t.Fatal(err)
			}
			file := minimalStickerWebP(t)
			if delegated {
				resp, err := executeDelegatedSticker(t.Context(), a, sendDelegateRequest{To: lid.String(), File: file, KeepUnread: true})
				if err != nil || resp.StoreWarning != "" || !resp.Sent {
					t.Fatalf("delegated sticker: %+v, %v", resp, err)
				}
			} else {
				res, err := sendSticker(t.Context(), a, lid, file, sendStickerOptions{keepUnread: true})
				if err != nil || res.storeWarning != nil || res.id != "sent" {
					t.Fatalf("direct sticker: %+v, %v", res, err)
				}
			}
			assertSendUnread(t, db, phone.String(), true, 2)
			// Sync canonicalizes the echo to the phone JID. It must find the
			// accepted outbound row, not mistake the echo for a fresh reply.
			if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: phone.String(), MsgID: "sent", Timestamp: time.Now(), FromMe: true, MediaType: "sticker", DisplayText: "Sent sticker"}); err != nil {
				t.Fatal(err)
			}
			assertSendUnread(t, db, phone.String(), true, 2)
		})
	}
}

func TestSendKeepUnreadCanonicalHistory(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, _, file := newSendFileFixture(t)
	for _, tc := range keepUnreadSendCases(t, file) {
		for _, delegated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delegated=%v", tc.name, delegated), func(t *testing.T) {
				real, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(real.Close)
				phone := types.NewJID("15550000001", types.DefaultUserServer)
				lid := types.NewJID("20001", types.HiddenUserServer)
				client := &keepUnreadWA{fakeSendFileWA: &fakeSendFileWA{sentID: "sent", mappedChat: phone}}
				a := &keepUnreadApp{App: real, client: client}
				db := a.DB()
				for _, chat := range []types.JID{phone, lid} {
					if err := db.UpsertChat(chat.String(), "dm", "Fixture", time.Now().Add(-time.Minute)); err != nil {
						t.Fatal(err)
					}
				}
				// Model a selection target still stored under its older LID alias.
				button := store.Button{Type: "quick_reply", ID: "yes", DisplayText: "Yes", ResponseType: selectResponseButtons}
				if tc.name == "select_list" {
					button.Type, button.ResponseType = "list_row", selectResponseList
				}
				if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: lid.String(), MsgID: "incoming", Timestamp: time.Now().Add(-time.Minute), Text: "Please answer", Buttons: []store.Button{button}}); err != nil {
					t.Fatal(err)
				}
				if err := db.SetChatUnreadCount(phone.String(), 2); err != nil {
					t.Fatal(err)
				}
				flags := &rootFlags{storeDir: a.StoreDir(), asJSON: true, timeout: 5 * time.Second}
				open := func(context.Context) (outboundSendApp, func(), error) { return a, func() {}, nil }
				if delegated {
					stop, err := startSendDelegateServerForStore(t.Context(), a.StoreDir(), sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
						return tc.execute(ctx, a, req)
					})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(stop)
					open = func(context.Context) (outboundSendApp, func(), error) { return nil, nil, lock.ErrLocked }
				}
				cmd := tc.newCmd(flags, open)
				cmd.SetArgs(append([]string{"--to", lid.String(), "--keep-unread", "--post-send-wait", "0"}, tc.args...))
				captureRootStdout(t, func() {
					if err := cmd.Execute(); err != nil {
						t.Fatal(err)
					}
				})
				assertSendUnread(t, db, phone.String(), true, 2)
				stored, err := db.GetMessage(phone.String(), "sent")
				if err != nil {
					t.Fatalf("outbound row must use sync's canonical chat key so its echo is not fresh: %v", err)
				}
				if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: phone.String(), MsgID: stored.MsgID, Timestamp: stored.Timestamp, FromMe: true, Text: stored.Text, DisplayText: stored.DisplayText, MediaType: stored.MediaType, ReactionToID: stored.ReactionToID, ReactionEmoji: stored.ReactionEmoji}); err != nil {
					t.Fatal(err)
				}
				assertSendUnread(t, db, phone.String(), true, 2)
			})
		}
	}
}

func TestSendKeepUnreadOldOwnerFailsClosed(t *testing.T) {
	_, _, file := newSendFileFixture(t)
	for _, tc := range keepUnreadSendCases(t, file) {
		t.Run(tc.name, func(t *testing.T) {
			flags := &rootFlags{storeDir: t.TempDir(), timeout: 5 * time.Second}
			var sent atomic.Bool
			stop, err := startSendDelegateServerForStore(t.Context(), flags.storeDir, sendSpacing{}, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				// v0.26.0's guard, before its send-kind switch. Unknown JSON
				// fields alone would be ignored and the send would mark read.
				if req.Version != 1 {
					return sendDelegateResponse{}, fmt.Errorf("unsupported send delegate version %d", req.Version)
				}
				sent.Store(true)
				return sendDelegateResponse{OK: true, Sent: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(stop)
			cmd := tc.newCmd(flags, func(context.Context) (outboundSendApp, func(), error) { return nil, nil, lock.ErrLocked })
			args := append([]string{"--to", "15550000001@s.whatsapp.net", "--post-send-wait", "0", "--keep-unread"}, tc.args...)
			cmd.SetArgs(args)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			err = cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "upgrade wacli and restart sync --follow") || !strings.Contains(err.Error(), "not sent") || sent.Load() {
				t.Fatalf("old owner must refuse without sending: sent=%v error=%v", sent.Load(), err)
			}
			// Ordinary clients still use v1 and remain compatible with old owners.
			resp, err := delegateSend(t.Context(), flags, sendDelegateRequest{Kind: "text", Message: "ordinary"})
			if err != nil || !resp.Sent || !sent.Load() {
				t.Fatalf("unflagged v1 request changed: %+v, sent=%v, error=%v", resp, sent.Load(), err)
			}
		})
	}
}
