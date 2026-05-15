//go:build darwin

package syscontacts

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

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

	cmd := swiftCommand(ctx, script)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("read macOS Contacts: %s", string(ee.Stderr))
		}
		return nil, fmt.Errorf("run swift Contacts helper: %w", err)
	}
	return Decode(bytes.NewReader(out))
}

func swiftCommand(ctx context.Context, script string) *exec.Cmd {
	if path, err := exec.LookPath("swift"); err == nil {
		return exec.CommandContext(ctx, path, script)
	}
	return exec.CommandContext(ctx, "xcrun", "swift", script)
}
