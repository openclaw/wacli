package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/config"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

var version = "0.8.0"

const docsURL = "https://wacli.sh"

type rootFlags struct {
	storeDir   string
	account    string
	asJSON     bool
	fullOutput bool
	events     bool
	timeout    time.Duration
	readOnly   bool
	lockWait   time.Duration
}

func execute(args []string) error {
	var flags rootFlags

	rootCmd := &cobra.Command{
		Use:           "wacli",
		Short:         "WhatsApp CLI: sync, search, send",
		Long:          "wacli is a WhatsApp CLI for syncing, searching, and sending from local scripts.\n\nDocs: " + docsURL,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	rootCmd.SetVersionTemplate("wacli {{.Version}}\n")

	rootCmd.PersistentFlags().StringVar(&flags.storeDir, "store", "", "store directory (default: $WACLI_STORE_DIR, XDG state dir on Linux, or ~/.wacli)")
	rootCmd.PersistentFlags().StringVar(&flags.account, "account", "", "named account from config.yaml")
	rootCmd.PersistentFlags().BoolVar(&flags.asJSON, "json", false, "output JSON instead of human-readable text")
	rootCmd.PersistentFlags().BoolVar(&flags.fullOutput, "full", false, "disable truncation in table output")
	rootCmd.PersistentFlags().BoolVar(&flags.events, "events", false, "emit machine-readable NDJSON lifecycle events on stderr")
	rootCmd.PersistentFlags().DurationVar(&flags.timeout, "timeout", 5*time.Minute, "command timeout (non-sync commands)")
	rootCmd.PersistentFlags().DurationVar(&flags.lockWait, "lock-wait", 0, "wait for the store lock before failing (write commands)")
	rootCmd.PersistentFlags().BoolVar(&flags.readOnly, "read-only", false, "reject commands that intentionally write WhatsApp or the local store (or set WACLI_READONLY=1)")

	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newAccountsCmd(&flags))
	rootCmd.AddCommand(newDoctorCmd(&flags))
	rootCmd.AddCommand(newAuthCmd(&flags))
	rootCmd.AddCommand(newSyncCmd(&flags))
	rootCmd.AddCommand(newMessagesCmd(&flags))
	rootCmd.AddCommand(newSendCmd(&flags))
	rootCmd.AddCommand(newMediaCmd(&flags))
	rootCmd.AddCommand(newContactsCmd(&flags))
	rootCmd.AddCommand(newChatsCmd(&flags))
	rootCmd.AddCommand(newGroupsCmd(&flags))
	rootCmd.AddCommand(newChannelsCmd(&flags))
	rootCmd.AddCommand(newHistoryCmd(&flags))
	rootCmd.AddCommand(newPresenceCmd(&flags))
	rootCmd.AddCommand(newProfileCmd(&flags))
	rootCmd.AddCommand(newDocsCmd(&flags))
	rootCmd.AddCommand(newStoreCmd(&flags))

	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		writeRootError(flags, err)
		return err
	}
	return nil
}

func writeRootError(flags rootFlags, err error) {
	if err == nil {
		return
	}
	if flags.events {
		_ = out.NewEventWriter(os.Stderr, true).Emit("error", map[string]any{"message": err.Error()})
		return
	}
	_ = out.WriteError(os.Stderr, flags.asJSON, err)
}

func newApp(ctx context.Context, flags *rootFlags, needLock bool, allowUnauthed bool) (*app.App, *lock.Lock, error) {
	storeDir, err := resolveStoreDir(flags)
	if err != nil {
		return nil, nil, err
	}

	var lk *lock.Lock
	if needLock {
		lk, err = lock.AcquireWithTimeout(ctx, storeDir, flags.lockWait)
		if err != nil {
			return nil, nil, err
		}
	}

	a, err := app.New(app.Options{
		StoreDir:      storeDir,
		Version:       version,
		JSON:          flags.asJSON,
		Events:        out.NewEventWriter(os.Stderr, flags.events),
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

func resolveStoreDir(flags *rootFlags) (string, error) {
	storeDir := ""
	account := ""
	if flags != nil {
		storeDir = flags.storeDir
		account = strings.TrimSpace(flags.account)
	}
	if storeDir != "" && account != "" {
		return "", fmt.Errorf("--store and --account cannot be combined")
	}
	switch {
	case storeDir != "":
	case account != "":
		resolved, _, err := config.ResolveAccountStore(config.DefaultConfigPath(), account)
		if err != nil {
			return "", err
		}
		storeDir = resolved
	case os.Getenv(config.EnvStoreDir) != "":
		storeDir = config.DefaultStoreDir()
	default:
		cfg, found, err := config.LoadAccountsConfigIfExists(config.DefaultConfigPath())
		if err != nil {
			return "", err
		}
		if found && strings.TrimSpace(cfg.DefaultAccount) != "" {
			resolved, _, err := config.ResolveAccountStore(config.DefaultConfigPath(), cfg.DefaultAccount)
			if err != nil {
				return "", err
			}
			storeDir = resolved
		} else {
			storeDir = config.DefaultStoreDir()
		}
	}
	storeDir, _ = filepath.Abs(storeDir)
	return storeDir, nil
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
