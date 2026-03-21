package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/lock"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
)

var version = "0.5.0"

type rootFlags struct {
	storeDir string
	asJSON   bool
	timeout  time.Duration
	readOnly bool
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

	rootCmd.PersistentFlags().StringVar(&flags.storeDir, "store", "", "store directory (default: ~/.wacli)")
	rootCmd.PersistentFlags().BoolVar(&flags.asJSON, "json", false, "output JSON instead of human-readable text")
	rootCmd.PersistentFlags().DurationVar(&flags.timeout, "timeout", 5*time.Minute, "command timeout (non-sync commands)")
	// Load persisted read-only setting from DB on startup
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		storeDir := flags.storeDir
		if storeDir == "" {
			storeDir = config.DefaultStoreDir()
		}
		storeDir, _ = filepath.Abs(storeDir)
		dbPath := filepath.Join(storeDir, "wacli.db")

		if _, err := os.Stat(dbPath); err == nil {
			db, err := store.Open(dbPath)
			if err == nil {
				if db.IsReadOnly() {
					flags.readOnly = true
				}
				_ = db.Close()
			}
		}
		return nil
	}

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
		lk, err = lock.Acquire(storeDir)
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

// errReadOnly is returned when a write operation is attempted in read-only mode.
var errReadOnly = errors.New("wacli is running in read-only mode (set during `wacli auth --readonly`); write operations are disabled")

func checkReadOnly(flags *rootFlags) error {
	if flags.readOnly {
		return errReadOnly
	}
	return nil
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
