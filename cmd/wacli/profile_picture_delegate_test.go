package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type fakePictureGetter struct {
	calls []types.JID
	info  *types.ProfilePictureInfo
	err   error
}

func (f *fakePictureGetter) GetProfilePictureInfo(_ context.Context, jid types.JID, _ bool, _ string) (*types.ProfilePictureInfo, error) {
	f.calls = append(f.calls, jid)
	return f.info, f.err
}

func pictureRequest(to string) sendDelegateRequest {
	return sendDelegateRequest{Version: sendDelegateVersion, Kind: profilePictureKind, To: to}
}

func TestPictureBudgetOffByDefault(t *testing.T) {
	getter := &fakePictureGetter{info: &types.ProfilePictureInfo{ID: "1", URL: "https://pps.whatsapp.net/x"}}
	_, err := executeDelegatedProfilePicture(context.Background(), getter, newPictureBudget(t.TempDir(), 0), pictureRequest("15551234567@s.whatsapp.net"))
	if !errors.Is(err, errProfilePictureDelegateOff) {
		t.Fatalf("error = %v, want off", err)
	}
	if len(getter.calls) != 0 {
		t.Fatalf("queried WhatsApp with the budget off")
	}
}

func TestPictureBudgetCapsPerDayAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	b := newPictureBudget(dir, 2)
	b.now = func() time.Time { return day }
	if err := b.take(); err != nil {
		t.Fatal(err)
	}
	// A restarted follower keeps today's count.
	b = newPictureBudget(dir, 2)
	b.now = func() time.Time { return day }
	if err := b.take(); err != nil {
		t.Fatal(err)
	}
	if err := b.take(); err == nil || !strings.Contains(err.Error(), "daily profile picture budget of 2 used") {
		t.Fatalf("third take = %v, want budget used", err)
	}
	info, err := os.Stat(filepath.Join(dir, profilePictureBudgetFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("budget file = %v %v, want 0600", info, err)
	}
	b.now = func() time.Time { return day.Add(24 * time.Hour) }
	if err := b.take(); err != nil {
		t.Fatalf("next day take = %v", err)
	}
}

func TestDelegatedPictureOnlyForPeople(t *testing.T) {
	for _, to := range []string{"120363000000000000@g.us", "status@broadcast", "123@newsletter"} {
		getter := &fakePictureGetter{}
		budget := newPictureBudget(t.TempDir(), 5)
		if _, err := executeDelegatedProfilePicture(context.Background(), getter, budget, pictureRequest(to)); err == nil {
			t.Fatalf("%s: accepted", to)
		}
		if len(getter.calls) != 0 || budget.used != 0 {
			t.Fatalf("%s: queried or spent budget", to)
		}
	}
	getter := &fakePictureGetter{info: &types.ProfilePictureInfo{ID: "9", URL: "https://pps.whatsapp.net/v/x.jpg", Type: "image"}}
	resp, err := executeDelegatedProfilePicture(context.Background(), getter, newPictureBudget(t.TempDir(), 5), pictureRequest("123456789@lid"))
	if err != nil || resp.Picture == nil || resp.Picture.URL != "https://pps.whatsapp.net/v/x.jpg" || resp.Picture.ID != "9" {
		t.Fatalf("lid picture = %+v %v", resp, err)
	}
	if getter.calls[0].String() != "123456789@lid" {
		t.Fatalf("queried %v", getter.calls)
	}
}

func TestDelegatedPictureHiddenOrMissingIsNone(t *testing.T) {
	for _, cause := range []error{whatsmeow.ErrProfilePictureNotSet, whatsmeow.ErrProfilePictureUnauthorized} {
		getter := &fakePictureGetter{err: cause}
		resp, err := executeDelegatedProfilePicture(context.Background(), getter, newPictureBudget(t.TempDir(), 5), pictureRequest("+15551234567"))
		if err != nil || !resp.OK || resp.Picture == nil || !resp.Picture.None || resp.Picture.JID != "15551234567@s.whatsapp.net" {
			t.Fatalf("%v: resp = %+v err = %v", cause, resp, err)
		}
	}
}

func TestReadOnlyPictureInfoDelegatesWithoutTheStore(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir) // the follower owns the store
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()
	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, Picture: &profilePictureInfoOutput{JID: req.To, ID: "7", URL: "https://pps.whatsapp.net/v/p.jpg"}}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--read-only", "--json", "--timeout", "750ms",
		"profile", "picture-info", "--jid", "+15551234567",
	})
	if err != nil {
		t.Fatalf("picture-info failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	req := server.nextRequest(t)
	if req.Kind != profilePictureKind || req.To != "15551234567@s.whatsapp.net" {
		t.Fatalf("delegate request = %+v", req)
	}
	if !strings.Contains(stdout, `"url":"https://pps.whatsapp.net/v/p.jpg"`) || !strings.Contains(stdout, `"id":"7"`) {
		t.Fatalf("stdout %q missing picture", stdout)
	}
}

func TestReadOnlyPictureInfoFailsClosedWithoutFollower(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--read-only", "--json", "--timeout", "750ms",
		"profile", "picture-info", "--jid", "+15551234567",
	})
	if err == nil || !strings.Contains(stdout+stderr, "needs a running") {
		t.Fatalf("err=%v stdout=%q stderr=%q, want fail closed", err, stdout, stderr)
	}
	if _, statErr := os.Stat(filepath.Join(storeDir, "session.db")); statErr == nil {
		t.Fatalf("read-only picture-info opened the session store")
	}
}

func TestReadOnlyStillRefusesOtherLiveProfileQueries(t *testing.T) {
	storeDir := shortPresenceDelegateStoreDir(t)
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--read-only", "--json", "--timeout", "750ms",
		"profile", "get-about", "--jid", "+15551234567",
	})
	if err == nil || !strings.Contains(stdout+stderr, "read-only mode") {
		t.Fatalf("get-about err=%v stdout=%q stderr=%q, want read-only refusal", err, stdout, stderr)
	}
}

func TestReadOnlyPictureInfoRefusesGroups(t *testing.T) {
	storeDir := shortPresenceDelegateStoreDir(t)
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--read-only", "--json", "--timeout", "750ms",
		"profile", "picture-info", "--jid", "120363000000000000@g.us",
	})
	if err == nil || !strings.Contains(stdout+stderr, "people only") {
		t.Fatalf("group err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
}

func TestPictureBudgetFailsClosedOnUntrustedState(t *testing.T) {
	for name, content := range map[string]string{
		"bad count": "2026-10-24 invalid\n",
		"bad day":   "yesterday 3\n",
		"negative":  "2026-10-24 -1\n",
		"extra":     "2026-10-24 1 2\n",
		"empty":     "",
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, profilePictureBudgetFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		getter := &fakePictureGetter{info: &types.ProfilePictureInfo{ID: "1", URL: "https://pps.whatsapp.net/x"}}
		_, err := executeDelegatedProfilePicture(context.Background(), getter, newPictureBudget(dir, 5), pictureRequest("15551234567@s.whatsapp.net"))
		if err == nil || !strings.Contains(err.Error(), "fix or remove it") {
			t.Fatalf("%s: error = %v, want fail closed", name, err)
		}
		if len(getter.calls) != 0 {
			t.Fatalf("%s: queried WhatsApp with an untrusted budget", name)
		}
		if raw, _ := os.ReadFile(filepath.Join(dir, profilePictureBudgetFile)); string(raw) != content {
			t.Fatalf("%s: budget file rewritten to %q", name, raw)
		}
	}

	// Unreadable (here: a directory where the file belongs) is refused too.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, profilePictureBudgetFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := newPictureBudget(dir, 5).take(); err == nil || !strings.Contains(err.Error(), "fix or remove it") {
		t.Fatalf("unreadable budget: error = %v, want fail closed", err)
	}
	// With the budget off, the file is not even read.
	if err := newPictureBudget(dir, 0).take(); !errors.Is(err, errProfilePictureDelegateOff) {
		t.Fatalf("off budget: error = %v", err)
	}
}
