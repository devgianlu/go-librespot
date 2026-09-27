//go:build !darwin

package main

import "github.com/devgianlu/go-librespot/mpris"

// runDaemon runs the daemon; only macOS needs an event loop beside it.
func runDaemon(_ mpris.Server, run func()) {
	run()
}
