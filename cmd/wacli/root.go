package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/lock"
	"github.com/steipete/wacli/internal/out"
)

var version = "0.7.0"

type rootFlags struct {
	storeDir   string
	asJSON     bool
	fullOutput bool
	timeout    time.Duration
	readOnly   bool
	lockWait   time.Duration
}

func execute(args []string) error {
	var flags rootFlags

	rootCmd := &cobra.Command{
		Use:           "wacli",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	rootCmd.SetVersionTemplate("wacli {{.Version}}\n")

	rootCmd.PersistentFlags().StringVar(&flags.storeDir, "store", "", "store directory (default: $WACLI_STORE_DIR, XDG state dir on Linux, or ~/.wacli)")
	rootCmd.PersistentFlags().BoolVar(&flags.asJSON, "json", false, "output JSON instead of human-readable text")
	rootCmd.PersistentFlags().BoolVar(&flags.fullOutput, "full", false, "disable truncation in table output")
	rootCmd.PersistentFlags().DurationVar(&flags.timeout, "timeout", 5*time.Minute, "command timeout (non-sync commands)")
	rootCmd.PersistentFlags().DurationVar(&flags.lockWait, "lock-wait", 0, "wait for the store lock before failing (write commands)")
	rootCmd.PersistentFlags().BoolVar(&flags.readOnly, "read-only", false, "reject commands that intentionally write WhatsApp or the local store (or set WACLI_READONLY=1)")

	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newDoctorCmd(&flags))
	rootCmd.AddCommand(newAuthCmd(&flags))
	rootCmd.AddCommand(newSyncCmd(&flags))
	rootCmd.AddCommand(newMessagesCmd(&flags))
	rootCmd.AddCommand(newSendCmd(&flags))
	rootCmd.AddCommand(newMediaCmd(&flags))
	rootCmd.AddCommand(newContactsCmd(&flags))
	rootCmd.AddCommand(newChatsCmd(&flags))
	rootCmd.AddCommand(newGroupsCmd(&flags))
	rootCmd.AddCommand(newHistoryCmd(&flags))
	rootCmd.AddCommand(newPresenceCmd(&flags))
	rootCmd.AddCommand(newProfileCmd(&flags))

	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		_ = out.WriteError(os.Stderr, flags.asJSON, err)
		return err
	}
	return nil
}

func newApp(ctx context.Context, flags *rootFlags, needLock bool, allowUnauthed bool) (*app.App, *lock.Lock, error) {
	storeDir := flags.storeDir
	if storeDir == "" {
		storeDir = config.DefaultStoreDir()
	}
	storeDir, _ = filepath.Abs(storeDir)

	var lk *lock.Lock
	if needLock {
		var err error
		lk, err = lock.AcquireWithTimeout(ctx, storeDir, flags.lockWait)
		if err != nil {
			return nil, nil, err
		}
	}

	a, err := app.New(app.Options{
		StoreDir:      storeDir,
		Version:       version,
		JSON:          flags.asJSON,
		AllowUnauthed: allowUnauthed,
	})
	if err != nil {
		if lk != nil {
			_ = lk.Release()
		}
		return nil, nil, err
	}

	return a, lk, nil
}

func (f *rootFlags) isReadOnly() bool {
	if f == nil {
		return false
	}
	if f.readOnly {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WACLI_READONLY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (f *rootFlags) requireWritable() error {
	if f.isReadOnly() {
		return fmt.Errorf("read-only mode: command would intentionally modify WhatsApp or the local store")
	}
	return nil
}

func withTimeout(ctx context.Context, flags *rootFlags) (context.Context, context.CancelFunc) {
	if flags.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, flags.timeout)
}

func closeApp(a *app.App, lk *lock.Lock) {
	if a != nil {
		a.Close()
	}
	if lk != nil {
		_ = lk.Release()
	}
}

func wrapErr(err error, msg string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return fmt.Errorf("%s: %w", msg, err)
}
