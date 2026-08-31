//go:build !windows

package main

import (
	"os/signal"
	"syscall"
)

func configureOutputSignals() {
	// Let stdout writes return EPIPE so the output layer can handle closed readers.
	signal.Ignore(syscall.SIGPIPE)
}
