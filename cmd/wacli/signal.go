package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/steipete/wacli/internal/out"
)

// signalContext returns a context that is cancelled on the first SIGINT/SIGTERM.
// A second signal force-kills the process so that a stuck cleanup never leaves
// the user unable to get their terminal back.
func signalContext() (context.Context, context.CancelFunc) {
	return signalContextWithEvents(nil)
}

func signalContextWithEvents(events *out.EventWriter) (context.Context, context.CancelFunc) {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	return signalContextForChannel(events, sigCh, func() { signal.Stop(sigCh) }, os.Exit)
}

func signalContextForChannel(events *out.EventWriter, sigCh <-chan os.Signal, stopNotify func(), forceExit func(int)) (context.Context, context.CancelFunc) {
	ctx, ctxCancel := context.WithCancel(context.Background())
	go func() {
		sig := <-sigCh
		if events.Enabled() {
			_ = events.Emit("signal", map[string]any{"signal": sig.String(), "action": "shutdown"})
		} else {
			fmt.Fprintln(os.Stderr, "\nShutting down (interrupt again to force quit)...")
		}
		ctxCancel()

		sig = <-sigCh
		if events.Enabled() {
			_ = events.Emit("signal", map[string]any{"signal": sig.String(), "action": "force_quit"})
		} else {
			fmt.Fprintln(os.Stderr, "Force quit.")
		}
		forceExit(1)
	}()

	return ctx, func() {
		if stopNotify != nil {
			stopNotify()
		}
		ctxCancel()
	}
}
