//go:build test_unit

package daemon

import (
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/dealer"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func newTestAppPlayer(t *testing.T) *AppPlayer {
	t.Helper()

	log := &librespot.NullLogger{}
	server, err := NewStubApiServer(log)
	require.NoError(t, err)

	p := &AppPlayer{
		app:    &App{log: log, server: server},
		state:  &State{},
		player: &player.Player{},
		// Neither lane is started, so the work a command provokes stays in
		// its queue instead of reaching the network.
		statePush: newTestStatePushLane(nil),
		loader:    newTestLoaderLane(),
	}
	p.state.reset()
	return p
}

func transferCommand(t *testing.T, state *connectpb.TransferState) dealer.RequestPayload {
	t.Helper()

	data, err := proto.Marshal(state)
	require.NoError(t, err)

	// An absent payload means something else entirely: it is how Spotify makes
	// a device active without playing anything.
	require.NotEmpty(t, data)

	var req dealer.RequestPayload
	req.Command.Endpoint = "transfer"
	req.Command.Data = data
	return req
}

// A transfer whose session carries no context used to be dereferenced straight
// into a segfault. With no track to fall back on either, it is refused.
func TestHandlePlayerCommandTransferWithoutContextOrTrack(t *testing.T) {
	p := newTestAppPlayer(t)

	req := transferCommand(t, &connectpb.TransferState{
		CurrentSession: &connectpb.Session{},
		Playback:       &connectpb.Playback{},
	})

	require.Error(t, p.handlePlayerCommand(req))
	require.False(t, p.state.active)
	require.Nil(t, p.state.tracks)
}

// A queued or autoplayed track arrives on its own, with no context to take it
// from: the transfer is taken over a context holding that track alone.
func TestHandlePlayerCommandTransferWithoutContextPlaysTheTrack(t *testing.T) {
	const uri = "spotify:track:2FY7b99s15jUprqC0M5NCT"

	id, err := librespot.SpotifyIdFromUri(uri)
	require.NoError(t, err)

	for _, tt := range []struct {
		name  string
		track *connectpb.ContextTrack
	}{
		{name: "by uri", track: &connectpb.ContextTrack{Uri: uri}},
		{name: "by gid", track: &connectpb.ContextTrack{Gid: id.Id()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestAppPlayer(t)

			req := transferCommand(t, &connectpb.TransferState{
				CurrentSession: &connectpb.Session{},
				Playback: &connectpb.Playback{
					Timestamp:             1789403433449,
					PositionAsOfTimestamp: 47480,
					CurrentTrack:          tt.track,
				},
			})

			require.NoError(t, p.handlePlayerCommand(req))
			require.True(t, p.state.active)
			require.Equal(t, uri, p.state.player.ContextUri)
			require.NotNil(t, p.state.player.PlayOrigin)
			require.Equal(t, uri, p.state.player.Track.GetUri())

			// The position comes across too, so the track picks up where the
			// other device left it.
			require.EqualValues(t, 47480, p.state.player.PositionAsOfTimestamp)

			// And the context reaches the loader, which is what actually
			// resolves and plays it.
			require.Len(t, p.loader.queue, 1)
			require.Equal(t, "transfer "+uri, p.loader.queue[0].name)
		})
	}
}
