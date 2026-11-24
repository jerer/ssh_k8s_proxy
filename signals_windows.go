//go:build windows

package main

import "os"

func listenSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func proxySignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func agentSupported() bool { return false }
