//go:build test_unit

package daemon

import (
	"testing"

	"github.com/devgianlu/go-librespot/dealer"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	thisDevice  = "this-device"
	otherDevice = "other-device"
	thirdDevice = "third-device"
)

func clusterMessage(t *testing.T, activeDeviceId string, playerTimestamp int64) dealer.Message {
	t.Helper()

	payload, err := proto.Marshal(&connectpb.ClusterUpdate{
		Cluster: &connectpb.Cluster{
			ActiveDeviceId: activeDeviceId,
			PlayerState:    &connectpb.PlayerState{Timestamp: playerTimestamp},
		},
		UpdateReason: connectpb.ClusterUpdateReason_DEVICE_STATE_CHANGED,
	})
	require.NoError(t, err)

	return dealer.Message{Uri: "hm://connect-state/v1/cluster", Payload: payload}
}

func playCommand() dealer.RequestPayload {
	var req dealer.RequestPayload
	req.Command.Endpoint = "play"
	req.Command.PlayOrigin = &connectpb.PlayOrigin{}
	req.Command.Context = &connectpb.Context{
		Uri: "spotify:album:3KIlhMEiSYNn8VD7e6yglU",
		Pages: []*connectpb.ContextPage{{Tracks: []*connectpb.ContextTrack{
			{Uri: "spotify:track:3j7BhP71ROCpc9R3w9P9UE", Uid: "u0"},
		}}},
	}
	req.SentByDeviceId = "webapi-0123"
	return req
}

// playOverOtherDevice has another device hold the session, with its player
// state from paused, then plays on this one.
func playOverOtherDevice(t *testing.T, paused int64) *AppPlayer {
	t.Helper()

	p := newTestAppPlayer(t)
	p.app.cfg = &Config{}
	p.app.deviceId = thisDevice
	p.resolveTrackList = resolveWith(0)

	require.NoError(t, p.handleDealerMessage(clusterMessage(t, otherDevice, paused)))
	require.NoError(t, p.handlePlayerCommand(playCommand()))
	require.True(t, p.state.active)
	return p
}

// A play is gated like a transfer. The device it took over from can report its
// state once more on its way to going inactive, and be made active again for a
// moment: librespot does. The update that results carries the state it had
// before the play, so it is not a transfer away, and the play must not be
// dropped for it.
func TestPlayIsNotBouncedBackByTheDeviceItTookOverFrom(t *testing.T) {
	p := playOverOtherDevice(t, 1000)

	require.NoError(t, p.handleDealerMessage(clusterMessage(t, otherDevice, 1000)))

	require.True(t, p.state.active, "the old device repeating its state is no transfer away")
}

// Playing on the old device again gives it a newer state: that is a transfer
// away like any other.
func TestPlayYieldsWhenTheOldDevicePlaysAgain(t *testing.T) {
	p := playOverOtherDevice(t, 1000)

	require.NoError(t, p.handleDealerMessage(clusterMessage(t, otherDevice, 2000)))

	require.False(t, p.state.active)
}

// Another device taking over reports a state newer than the play's.
func TestPlayYieldsToAThirdDevice(t *testing.T) {
	p := playOverOtherDevice(t, 1000)

	require.NoError(t, p.handleDealerMessage(clusterMessage(t, thirdDevice, 1500)))

	require.False(t, p.state.active)
}
