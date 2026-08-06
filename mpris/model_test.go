//go:build test_unit

package mpris_test

import (
	"testing"

	"github.com/devgianlu/go-librespot/mpris"
	"github.com/stretchr/testify/require"
)

// Repeat-track wins over repeat-context, matching the player: with both set
// Spotify repeats the single track.
func TestGetLoopStatus(t *testing.T) {
	for _, tt := range []struct {
		name             string
		repeatingContext bool
		repeatingTrack   bool
		want             mpris.LoopStatus
	}{
		{"neither", false, false, mpris.None},
		{"context only", true, false, mpris.Playlist},
		{"track only", false, true, mpris.Track},
		{"track wins over context", true, true, mpris.Track},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, mpris.GetLoopStatus(tt.repeatingContext, tt.repeatingTrack))
		})
	}
}

// The values are sent over D-Bus verbatim, so they have to stay exactly the
// strings the MPRIS spec defines.
func TestLoopStatusWireValues(t *testing.T) {
	require.Equal(t, mpris.LoopStatus("None"), mpris.None)
	require.Equal(t, mpris.LoopStatus("Playlist"), mpris.Playlist)
	require.Equal(t, mpris.LoopStatus("Track"), mpris.Track)
}

// The daemon falls back to DummyServer whenever D-Bus is unavailable, so
// every method has to stay safe to call on it.
func TestDummyServerIsInert(t *testing.T) {
	var server mpris.Server = mpris.DummyServer{}

	require.NotPanics(t, func() {
		server.EmitStateUpdate(mpris.MediaState{})
		server.EmitSeekUpdate(mpris.SeekState{})
	})
	require.NoError(t, server.Close())

	// Receive must hand back a channel that never fires rather than nil: the
	// daemon selects on it forever.
	ch := server.Receive()
	require.NotNil(t, ch)
	select {
	case <-ch:
		t.Fatal("dummy server should never emit a command")
	default:
	}
}
