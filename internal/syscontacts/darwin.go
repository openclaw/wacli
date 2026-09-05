//go:build darwin

package syscontacts

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
)

//go:embed contacts_export.swift
var contactsExportSwift string

func ReadSystem(ctx context.Context) ([]Contact, error) {
	dir, err := os.MkdirTemp("", "wacli-contacts-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	script := filepath.Join(dir, "contacts-export.swift")
	if err := fsutil.WritePrivateFile(script, []byte(contactsExportSwift)); err != nil {
		return nil, err
	}

	return readContactsCommand(swiftCommand(ctx, script))
}

func readContactsCommand(cmd *exec.Cmd) ([]Contact, error) {
	// Swift can spawn a compiler or script child that inherits its output pipes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error { return stopContactsCommand(cmd) }
	}
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open swift Contacts helper output: %w", err)
	}
	// Output normally bounds captured stderr; retain that bound when streaming stdout.
	stderr := &contactsStderr{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("run swift Contacts helper: %w", err)
	}
	raw, readErr := readContactsExport(stdout)
	if readErr != nil {
		// Stop the producer at the byte budget before waiting for it.
		_ = stdout.Close()
		_ = stopContactsCommand(cmd)
	}
	err = cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("read macOS Contacts: %s", stderr.String())
		}
		return nil, fmt.Errorf("run swift Contacts helper: %w", err)
	}
	return Decode(bytes.NewReader(raw))
}

func stopContactsCommand(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

type contactsStderr struct{ buffer bytes.Buffer }

func (b *contactsStderr) String() string { return b.buffer.String() }

func (b *contactsStderr) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 64 << 10
	if remaining := limit - b.buffer.Len(); remaining > 0 {
		_, _ = b.buffer.Write(p[:min(n, remaining)])
	}
	return n, nil
}

func swiftCommand(ctx context.Context, script string) *exec.Cmd {
	if path, err := exec.LookPath("swift"); err == nil {
		return exec.CommandContext(ctx, path, script)
	}
	return exec.CommandContext(ctx, "xcrun", "swift", script)
}
