package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
)

func newAgentCmd(flags *rootFlags) *cobra.Command {
	var autoPresence bool

	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run as a JSON-RPC 2.0 agent (stdin/stdout)",
		Long: `Start a long-running agent that communicates via JSON-RPC 2.0 over NDJSON.

Reads requests from stdin and writes responses/notifications to stdout.
Logs and errors go to stderr. Requires prior authentication (wacli auth).

Use --auto-presence to simulate human-like presence when sending messages
(goes online, shows typing indicator, sends, goes offline).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}

			return a.RunAgent(ctx, os.Stdin, os.Stdout, app.AgentOptions{
				AutoPresence: autoPresence,
				TypingDelay:  autoPresence,
			})
		},
	}

	cmd.Flags().BoolVar(&autoPresence, "auto-presence", true, "simulate human-like presence when sending messages")
	return cmd
}
