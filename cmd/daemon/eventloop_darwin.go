//go:build darwin

package main

import (
	"runtime"

	"github.com/devgianlu/go-librespot/mpris"
)

func init() {
	// macOS delivers media remote commands through the main thread's event
	// loop, so the main goroutine has to stay on the main thread.
	runtime.LockOSThread()
}

// runDaemon runs the daemon, next to the macOS event loop when the Now
// Playing integration is on.
func runDaemon(mediaPlayer mpris.Server, run func()) {
	if _, ok := mediaPlayer.(*mpris.NowPlayingServer); ok {
		mpris.RunWithEventLoop(run)
		return
	}
	run()
}
