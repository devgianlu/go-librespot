//go:build test_unit

package daemon

import (
	"testing"
	"time"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/require"
)

func TestConnectDevicesNoCluster(t *testing.T) {
	p := &AppPlayer{app: &App{deviceId: thisDevice}}
	require.Nil(t, p.connectDevices(time.Now()))
	require.ErrorIs(t, p.remoteDevice(otherDevice, false), ErrNotFound)
	p.setCluster(nil)
	require.Nil(t, p.cluster)
}

func TestConnectDevicesListsClusterAndExtrapolates(t *testing.T) {
	p := &AppPlayer{app: &App{deviceId: thisDevice}}
	now := time.UnixMilli(1_000_000)
	p.setCluster(&connectpb.Cluster{
		ActiveDeviceId: otherDevice,
		Device: map[string]*connectpb.DeviceInfo{
			thisDevice:  {Name: "me"},
			otherDevice: {Name: "phone", Volume: 100, DisallowTransferReasons: []string{"x"}},
		},
		PlayerState: &connectpb.PlayerState{
			Timestamp: 998_000, PositionAsOfTimestamp: 5_000, Duration: 60_000, IsPlaying: true,
			Track: &connectpb.ProvidedTrack{Uri: "spotify:track:a", Metadata: map[string]string{"title": "T"}},
		},
	})

	got := p.connectDevices(now)
	require.Len(t, got.Devices, 2)
	require.True(t, !got.Devices[0].IsSelf && got.Devices[0].IsActive && !got.Devices[0].CanTransfer)
	require.Equal(t, int64(7_000), got.ApiConnectNowPlaying.Position)
	require.Equal(t, "T", got.ApiConnectNowPlaying.TrackName)
	require.True(t, got.ApiConnectNowPlaying.Playing)

	require.NoError(t, p.remoteDevice(otherDevice, false))
	require.ErrorIs(t, p.remoteDevice(thisDevice, false), ErrBadRequest)
	require.NoError(t, p.remoteDevice(thisDevice, true))
}

func TestConnectControlRequest(t *testing.T) {
	m, k, _, err := connectControlRequest(ApiRequest{Data: ApiConnectCommand{Endpoint: "set_volume", Volume: 100}})
	require.NoError(t, err)
	require.Equal(t, "PUT", m)
	require.Equal(t, "connect/volume", k)

	_, _, _, err = connectControlRequest(ApiRequest{Data: ApiConnectCommand{Endpoint: "set_volume", Volume: 70000}})
	require.ErrorIs(t, err, ErrBadRequest)
	_, _, _, err = connectControlRequest(ApiRequest{Data: ApiConnectCommand{Endpoint: "play", Uri: "http://x"}})
	require.ErrorIs(t, err, ErrBadRequest)
	_, _, _, err = connectControlRequest(ApiRequest{Data: ApiConnectCommand{Endpoint: "nope"}})
	require.ErrorIs(t, err, ErrBadRequest)
}

func TestConnectControlRequestPlay(t *testing.T) {
	command := func(body any) map[string]any {
		return body.(map[string]any)["command"].(map[string]any)
	}

	_, _, body, err := connectControlRequest(ApiRequest{Data: ApiConnectCommand{Endpoint: "play", Uri: "spotify:album:a"}})
	require.NoError(t, err)
	options := command(body)["options"].(map[string]any)
	require.Empty(t, options["skip_to"])
	require.NotContains(t, options, "initially_paused")

	_, kind, body, err := connectControlRequest(ApiRequest{Data: ApiConnectCommand{
		Endpoint: "play", Uri: "spotify:album:a", SkipToUri: "spotify:track:b", Paused: true,
	}})
	require.NoError(t, err)
	require.Equal(t, "player/command", kind)
	options = command(body)["options"].(map[string]any)
	require.Equal(t, map[string]any{"track_uri": "spotify:track:b"}, options["skip_to"])
	require.Equal(t, true, options["initially_paused"])

	_, _, _, err = connectControlRequest(ApiRequest{Data: ApiConnectCommand{Endpoint: "play", Uri: "spotify:album:a", SkipToUri: "http://x"}})
	require.ErrorIs(t, err, ErrBadRequest)
}
