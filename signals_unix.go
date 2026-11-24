//go:build !windows

package main

import (
	"os"
	"syscall"
)

func listenSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func proxySignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

func agentSupported() bool { return true }
