package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/openclaw/wacli/internal/wa"
)

type pictureFake struct {
	jid     types.JID
	preview bool
	calls   int
	err     error
}

func (f *pictureFake) LinkedJID() string { return "123:4@s.whatsapp.net" }
func (f *pictureFake) GetProfilePictureInfo(_ context.Context, jid types.JID, preview bool, _ string) (*types.ProfilePictureInfo, error) {
	f.jid, f.preview = jid, preview
	f.calls++
	return &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/photo", ID: "p1", Type: "image", DirectPath: "/photo"}, f.err
}

func TestProfilePictureInfo(t *testing.T) {
	for _, tc := range []struct {
		jid, code string
		err       error
	}{
		{"", "", nil}, {"123@lid", "", nil}, {"123-456@g.us", "", nil},
		{"status@broadcast", "invalid_jid", nil}, {"123@newsletter", "invalid_jid", nil},
		{"123", "invalid_jid", nil}, {"@s.whatsapp.net", "invalid_jid", nil},
		{"123@s.whatsapp.net ", "invalid_jid", nil},
		{"", "not_set", fmt.Errorf("wrapped: %w", whatsmeow.ErrProfilePictureNotSet)},
		{"", "hidden", fmt.Errorf("wrapped: %w", whatsmeow.ErrProfilePictureUnauthorized)},
		{"", "timeout", context.DeadlineExceeded}, {"", "lookup_failed", fmt.Errorf("network")},
		// whatsmeow's own IQ deadline (75 s) fires before the command budget.
		{"", "timeout", fmt.Errorf("wrapped: %w", whatsmeow.ErrIQTimedOut)},
		{"", "unavailable", wa.ErrNotConnected},
	} {
		t.Run(tc.jid+tc.code, func(t *testing.T) {
			f := &pictureFake{err: tc.err}
			resp := executeProfilePictureInfo(context.Background(), f, sendDelegateRequest{JID: tc.jid, Preview: true})
			if resp.ErrorCode != tc.code || resp.OK != (tc.code == "") {
				t.Fatalf("%+v", resp)
			}
			if tc.code == "unavailable" && tc.err != nil && !strings.Contains(resp.Error, "reconnect and retry") {
				t.Fatalf("disconnected client gives no recovery action: %q", resp.Error)
			}
			if tc.code == "lookup_failed" && !strings.Contains(resp.Error, "network") {
				t.Fatalf("lookup failure drops its cause: %q", resp.Error)
			}
			if tc.code == "invalid_jid" && f.calls != 0 {
				t.Fatal("queried invalid JID")
			}
			if resp.OK && (resp.ProfilePicture.ID != "p1" || !f.preview || f.jid.Device != 0) {
				t.Fatalf("%+v / %+v", resp, f)
			}
		})
	}
}

func TestProfilePictureBypassesSendQueue(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	// Empty slot blocks every send, but not a picture lookup.
	go handleSendDelegateConn(context.Background(), server, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeProfilePictureInfo(ctx, &pictureFake{}, req), nil
	}, make(chan struct{}, 1), newSendPacer(sendSpacing{}))
	if err := json.NewEncoder(client).Encode(sendDelegateRequest{Version: 1, Kind: profilePictureInfoKind, TimeoutMS: 500}); err != nil {
		t.Fatal(err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ProfilePicture == nil {
		t.Fatalf("%+v", resp)
	}
}

type unchangedPictureFake struct{ existingID string }

func (f *unchangedPictureFake) LinkedJID() string { return "123@s.whatsapp.net" }
func (f *unchangedPictureFake) GetProfilePictureInfo(_ context.Context, _ types.JID, _ bool, existingID string) (*types.ProfilePictureInfo, error) {
	f.existingID = existingID
	return nil, nil
}

func TestProfilePictureInfoReportsAnUnchangedPicture(t *testing.T) {
	f := &unchangedPictureFake{}
	resp := executeProfilePictureInfo(context.Background(), f, sendDelegateRequest{JID: "123@s.whatsapp.net", ExistingID: "p1"})
	if !resp.OK || resp.ProfilePicture == nil || !resp.ProfilePicture.Unchanged || f.existingID != "p1" {
		t.Fatalf("%+v / %+v", resp, f)
	}
	output := delegatedProfilePictureOutput(types.NewJID("123", types.DefaultUserServer), resp.ProfilePicture)
	if !output.Unchanged || output.JID != "123@s.whatsapp.net" {
		t.Fatalf("%+v", output)
	}

	// Without an existing ID a nil answer is not "unchanged".
	if resp := executeProfilePictureInfo(context.Background(), f, sendDelegateRequest{}); resp.OK || resp.ErrorCode != "lookup_failed" {
		t.Fatalf("%+v", resp)
	}
}

func TestDelegatedProfilePictureMatchesTheDirectOutput(t *testing.T) {
	target := types.NewJID("123", types.DefaultUserServer)
	info := &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/photo", ID: "p1", Type: "image", DirectPath: "/photo", Hash: []byte{1, 2}}
	resp := executeProfilePictureInfo(context.Background(), &pictureFake{}, sendDelegateRequest{JID: target.String()})
	direct := formatProfilePictureInfo(target, info)
	delegated := delegatedProfilePictureOutput(target, resp.ProfilePicture)
	direct.Hash, delegated.Hash = "", ""
	if direct != delegated {
		t.Fatalf("direct %+v, delegated %+v", direct, delegated)
	}
}

func TestOldDaemonRejectionExplainsTheUpgrade(t *testing.T) {
	err := explainProfilePictureDelegateError(fmt.Errorf("unsupported send kind %q", profilePictureInfoKind))
	if err == nil || !strings.Contains(err.Error(), "restart `wacli sync` after upgrading") {
		t.Fatalf("%v", err)
	}
	if err := explainProfilePictureDelegateError(fmt.Errorf("profile picture is private")); !strings.Contains(err.Error(), "get profile picture info") {
		t.Fatalf("%v", err)
	}
}
