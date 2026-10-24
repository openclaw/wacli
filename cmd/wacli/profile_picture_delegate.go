package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Profile pictures through the running sync process.
//
// `profile picture-info` is the one live query read-only mode allows, and only
// by asking the same-store `sync --follow` process, which owns the session. The
// read-only caller never opens the store for writing and never connects itself.
// The follower answers only when started with --profile-picture-budget N, and
// then at most N queries per UTC day (kept in the store directory so restarts
// don't reset it). It asks for user pictures only: no groups, channels or
// broadcasts, and nothing is sent to the person (no message, receipt or presence).

const (
	profilePictureKind       = "profile_picture_info"
	profilePictureBudgetFile = ".picture-budget"
)

var errProfilePictureDelegateOff = errors.New("profile picture queries are off in this sync process; start it with --profile-picture-budget N")

type pictureBudget struct {
	mu     sync.Mutex
	max    int
	path   string
	day    string
	used   int
	broken error // the saved count can't be trusted: refuse instead of starting over
	now    func() time.Time
}

// newPictureBudget loads today's count from the store directory. max <= 0 turns
// delegated picture queries off. A missing file is a fresh start; an unreadable
// or malformed one makes every query fail closed until it is fixed or removed.
func newPictureBudget(storeDir string, max int) *pictureBudget {
	b := &pictureBudget{max: max, now: time.Now}
	if storeDir != "" {
		b.path = filepath.Join(storeDir, profilePictureBudgetFile)
	}
	if b.path == "" || max <= 0 {
		return b
	}
	raw, err := os.ReadFile(b.path)
	if errors.Is(err, os.ErrNotExist) {
		return b
	}
	if err != nil {
		b.broken = fmt.Errorf("read profile picture budget %s: %w; fix or remove it", b.path, err)
		return b
	}
	fields := strings.Fields(string(raw))
	var n int
	if len(fields) == 2 {
		_, dayErr := time.Parse("2006-01-02", fields[0])
		n, err = strconv.Atoi(fields[1])
		if dayErr == nil && err == nil && n >= 0 {
			b.day, b.used = fields[0], n
			return b
		}
	}
	b.broken = fmt.Errorf("profile picture budget %s is not \"YYYY-MM-DD COUNT\"; fix or remove it", b.path)
	return b
}

// take spends one query of today's budget, or says why it can't.
func (b *pictureBudget) take() error {
	if b == nil || b.max <= 0 {
		return errProfilePictureDelegateOff
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.broken != nil {
		return b.broken
	}
	today := b.now().UTC().Format("2006-01-02")
	if b.day != today {
		b.day, b.used = today, 0
	}
	if b.used >= b.max {
		return fmt.Errorf("daily profile picture budget of %d used; try again tomorrow", b.max)
	}
	b.used++
	if b.path != "" {
		// Persist before the query: a crash must not hand out the same slot twice.
		if err := writePrivateFileAtomic(b.path, []byte(b.day+" "+strconv.Itoa(b.used)+"\n")); err != nil {
			b.used--
			return fmt.Errorf("record profile picture budget: %w", err)
		}
	}
	return nil
}

func writePrivateFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".picture-budget-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	// The count must be on disk before the query it pays for.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes a rename in dir durable. Windows has no directory fsync; there
// MoveFileEx (os.Rename) is the durable step.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type profilePictureGetter interface {
	GetProfilePictureInfo(ctx context.Context, jid types.JID, preview bool, existingID string) (*types.ProfilePictureInfo, error)
}

// parsePictureTarget accepts a phone number or a user JID (phone or LID), nothing else.
func parsePictureTarget(raw string) (types.JID, error) {
	target, err := parseProfileTarget(raw)
	if err != nil {
		return types.JID{}, err
	}
	if target.Server != types.DefaultUserServer && target.Server != types.HiddenUserServer {
		return types.JID{}, fmt.Errorf("profile pictures through sync are for people only, not %s", target.Server)
	}
	if target.User == "" {
		return types.JID{}, fmt.Errorf("--jid is required")
	}
	return target.ToNonAD(), nil
}

func executeDelegatedProfilePicture(ctx context.Context, getter profilePictureGetter, budget *pictureBudget, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.Version != sendDelegateVersion {
		return sendDelegateResponse{}, fmt.Errorf("unsupported send delegate version %d", req.Version)
	}
	target, err := parsePictureTarget(req.To)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if err := budget.take(); err != nil {
		return sendDelegateResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, millisDuration(req.TimeoutMS, time.Minute))
	defer cancel()
	info, err := getter.GetProfilePictureInfo(ctx, target, req.Preview, req.ExistingID)
	if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) || errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
		return sendDelegateResponse{OK: true, Picture: &profilePictureInfoOutput{JID: target.String(), None: true}}, nil
	}
	if err != nil {
		return sendDelegateResponse{}, fmt.Errorf("get profile picture info: %w", err)
	}
	output := formatProfilePictureInfo(target, info)
	return sendDelegateResponse{OK: true, Picture: &output}, nil
}

// runReadOnlyPictureInfo asks the running sync process; it never opens the store or connects.
func runReadOnlyPictureInfo(flags *rootFlags, target types.JID, preview bool, existingID string) error {
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()
	resp, err := delegateSend(ctx, flags, sendDelegateRequest{
		Kind:       profilePictureKind,
		To:         target.String(),
		Preview:    preview,
		ExistingID: existingID,
	})
	if errors.Is(err, errSendDelegateUnavailable) {
		return fmt.Errorf("read-only mode: profile picture-info needs a running `wacli sync --follow --profile-picture-budget N` on this store")
	}
	if err != nil {
		if strings.Contains(err.Error(), "unsupported send kind") && strings.Contains(err.Error(), profilePictureKind) {
			return fmt.Errorf("the running sync process does not support profile pictures; restart `wacli sync` after upgrading: %w", err)
		}
		return err
	}
	if resp.Picture == nil {
		return fmt.Errorf("the running sync process sent no picture info")
	}
	return writeProfilePictureInfo(flags, *resp.Picture)
}

func writeProfilePictureInfo(flags *rootFlags, output profilePictureInfoOutput) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, output)
	}
	switch {
	case output.None:
		fmt.Fprintf(os.Stdout, "%s has no profile picture you can see.\n", sanitize(output.JID))
	case output.Unchanged:
		fmt.Fprintf(os.Stdout, "%s profile picture is unchanged.\n", sanitize(output.JID))
	default:
		fmt.Fprintf(os.Stdout, "JID: %s\nID: %s\nType: %s\nURL: %s\nDirect path: %s\n", sanitize(output.JID), sanitize(output.ID), sanitize(output.Type), sanitize(output.URL), sanitize(output.DirectPath))
	}
	return nil
}
