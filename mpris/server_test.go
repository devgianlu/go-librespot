//go:build linux && test_unit

package mpris

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// startBus runs a private session bus listening at addr until the test ends or
// the returned stop is called.
func startBus(t *testing.T, addr string) (stop func()) {
	t.Helper()

	cmd := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address="+addr)
	require.NoError(t, cmd.Start())

	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	t.Cleanup(stop)
	return stop
}

// playbackStatus reads the server's PlaybackStatus off the bus at addr, or
// returns "" while it is not there.
func playbackStatus(t *testing.T, addr string) string {
	t.Helper()

	conn, err := dbus.Connect(addr)
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()

	v, err := conn.Object("org.mpris.MediaPlayer2.go-librespot", "/org/mpris/MediaPlayer2").
		GetProperty("org.mpris.MediaPlayer2.Player.PlaybackStatus")
	if err != nil {
		return ""
	}
	status, _ := v.Value().(string)
	return status
}

// A system service can start before the user's session bus exists. The server
// must not fail for it: it waits, shows up once the bus does, and comes back
// after losing it, with the latest state each time.
func TestServerWaitsForTheSessionBus(t *testing.T) {
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not installed")
	}

	prev := busRetryInterval
	busRetryInterval = 100 * time.Millisecond
	t.Cleanup(func() { busRetryInterval = prev })

	addr := "unix:path=" + filepath.Join(t.TempDir(), "bus")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	s, err := NewServer(&librespot.NullLogger{})
	require.NoError(t, err, "no bus yet is not an error")
	defer func() { require.NoError(t, s.Close()) }()

	s.EmitStateUpdate(MediaState{PlaybackStatus: Playing, LoopStatus: None})

	stop := startBus(t, addr)
	require.Eventually(t, func() bool { return playbackStatus(t, addr) == string(Playing) },
		5*time.Second, 50*time.Millisecond, "server did not show up once the bus did")

	stop()
	s.EmitStateUpdate(MediaState{PlaybackStatus: Paused, LoopStatus: None})

	startBus(t, addr)
	require.Eventually(t, func() bool { return playbackStatus(t, addr) == string(Paused) },
		5*time.Second, 50*time.Millisecond, "server did not come back after losing the bus")
}
