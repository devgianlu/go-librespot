//go:build test_unit

package daemon

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/dealer"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// remoteCluster is a cluster in which otherDevice plays trackUri(b).
func remoteCluster(b byte, paused bool, timestamp, position int64) *connectpb.Cluster {
	return &connectpb.Cluster{
		ActiveDeviceId: otherDevice,
		Device: map[string]*connectpb.DeviceInfo{
			otherDevice: {Name: "iPhone", DeviceType: devicespb.DeviceType_SMARTPHONE},
			thisDevice:  {Name: "this"},
		},
		PlayerState: &connectpb.PlayerState{
			Timestamp:             timestamp,
			PositionAsOfTimestamp: position,
			IsPlaying:             true,
			IsPaused:              paused,
			Track:                 &connectpb.ProvidedTrack{Uri: trackUri(b)},
		},
	}
}

func remoteClusterMessage(t *testing.T, c *connectpb.Cluster) dealer.Message {
	t.Helper()

	payload, err := proto.Marshal(&connectpb.ClusterUpdate{
		Cluster:      c,
		UpdateReason: connectpb.ClusterUpdateReason_DEVICE_STATE_CHANGED,
	})
	require.NoError(t, err)
	return dealer.Message{Uri: "hm://connect-state/v1/cluster", Payload: payload}
}

// newRemoteTestPlayer is an inactive player whose remote metadata comes from
// fetch.
func newRemoteTestPlayer(t *testing.T, fetch func(context.Context, string) (*librespot.Media, error)) *AppPlayer {
	t.Helper()

	p := newTestAppPlayer(t)
	p.ctx = t.Context()
	p.app.cfg = &Config{}
	p.app.deviceId = thisDevice
	p.prodInfo = &ProductInfo{}
	p.remoteMeta = make(chan remoteMetaResult, 1)
	p.fetchRemoteMedia = fetch
	return p
}

func fetchNamed(name string) func(context.Context, string) (*librespot.Media, error) {
	return func(context.Context, string) (*librespot.Media, error) {
		return librespot.NewMediaFromTrack(metaTrack(gid(0x01), name)), nil
	}
}

// applyFetched hands the detached fetch's result to the player, as the Run
// loop would.
func applyFetched(t *testing.T, p *AppPlayer) {
	t.Helper()

	select {
	case res := <-p.remoteMeta:
		p.applyRemoteMeta(res)
	case <-time.After(5 * time.Second):
		t.Fatal("the remote track's metadata was never fetched")
	}
}

func remoteEvents(p *AppPlayer) []*ApiRemote {
	var remotes []*ApiRemote
	for _, ev := range p.app.server.(*recordingApiServer).emitted {
		if ev.Type == ApiEventTypeRemote {
			remotes = append(remotes, ev.Data.(*ApiRemote))
		}
	}
	return remotes
}

// Another device playing is announced at once, and again once its track is
// named: the cluster has no artist, so the track waits on its metadata.
func TestRemotePlaybackIsAnnouncedThenNamed(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, time.Now().UnixMilli(), 1000))))
	applyFetched(t, p)

	events := remoteEvents(p)
	require.Len(t, events, 2)
	require.Equal(t, "iPhone", events[0].DeviceName)
	require.Equal(t, "SMARTPHONE", events[0].DeviceType)
	require.Nil(t, events[0].Track)
	require.Equal(t, "Sushi", events[1].Track.Name)
	require.Equal(t, []string{"Artist"}, events[1].Track.ArtistNames)

	require.Equal(t, otherDevice, p.apiRemote().DeviceId)
}

// The cluster is re-sent whenever any device changes; one that says nothing
// new about the remote playback raises nothing.
func TestRepeatedClusterRaisesNoRemoteEvent(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))
	c := remoteCluster(0x01, false, 1000, 0)

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, c)))
	applyFetched(t, p)
	c.Device[thirdDevice] = &connectpb.DeviceInfo{Name: "a device connecting"}
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, c)))

	require.Len(t, remoteEvents(p), 2)
}

// With no active device the cluster still carries the last player state; it
// describes nothing playing anywhere.
func TestNoActiveDeviceIsNoRemotePlayback(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, 1000, 0))))
	applyFetched(t, p)
	gone := remoteCluster(0x01, true, 1000, 0)
	gone.ActiveDeviceId = ""
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, gone)))

	events := remoteEvents(p)
	require.Nil(t, events[len(events)-1])
	require.Nil(t, p.apiRemote())
}

// An active id the cluster lists no device for is no active device either: the
// device left, and its last state describes nothing playing.
func TestActiveDeviceMissingFromTheClusterIsNoRemotePlayback(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, 1000, 0))))
	applyFetched(t, p)
	gone := remoteCluster(0x01, false, 1000, 0)
	delete(gone.Device, otherDevice)
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, gone)))

	events := remoteEvents(p)
	require.Nil(t, events[len(events)-1])
	require.Nil(t, p.apiRemote())
}

// Metadata that lands after the remote moved on to another track is not
// pinned on the new one.
func TestLateMetadataOfAnOldRemoteTrackIsDropped(t *testing.T) {
	p := newRemoteTestPlayer(t, func(ctx context.Context, uri string) (*librespot.Media, error) {
		if uri == trackUri(0x01) {
			return fetchNamed("first")(ctx, uri)
		}
		return nil, errors.New("offline")
	})

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, 1000, 0))))
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x02, false, 2000, 0))))
	applyFetched(t, p)

	require.Nil(t, p.apiRemote().Track)
	require.Equal(t, trackUri(0x02), p.remote.trackUri)
}

// The position moves with the remote playhead while it plays, and stays where
// it paused.
func TestRemotePositionFollowsItsAnchor(t *testing.T) {
	media := librespot.NewMediaFromTrack(metaTrack(gid(0x01), "x"))
	media.Track().Duration = proto.Int32(600000)

	playing := &remotePlayback{playing: true, timestamp: 10_000, positionAsOf: 5_000, media: media}
	require.EqualValues(t, 8_000, playing.position(13_000), "no speed given: 1")

	faster := &remotePlayback{playing: true, speed: 1.5, timestamp: 10_000, positionAsOf: 5_000, media: media}
	require.EqualValues(t, 9_500, faster.position(13_000), "a podcast played faster")

	paused := &remotePlayback{playing: true, paused: true, timestamp: 10_000, positionAsOf: 5_000, media: media}
	require.EqualValues(t, 5_000, paused.position(13_000))

	buffering := &remotePlayback{playing: true, buffering: true, speed: 1, timestamp: 10_000, positionAsOf: 5_000, media: media}
	require.EqualValues(t, 5_000, buffering.position(13_000))
	require.False(t, buffering.moving(), "reported paused: a client must not extrapolate")

	require.EqualValues(t, 600000, playing.position(10_000_000), "never past the end of the track")
}

// While this device is active it is the one playing: a cluster naming another
// device then is stale (it predates the activation) and shows no remote.
func TestActiveDeviceShowsNoRemote(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))
	p.remote = &remotePlayback{deviceId: otherDevice, trackUri: trackUri(0x01)}
	p.state.setActive(true)

	require.Nil(t, p.apiRemote())
}

// Becoming active forgets what played elsewhere and says so: a client trusting
// the event is told nothing plays elsewhere, and a local stop afterwards does
// not bring the phone's old record back to /status.
func TestBecomingActiveForgetsTheRemote(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, 1000, 0))))
	applyFetched(t, p)

	p.state.setActive(true)
	p.pushState(connectpb.PutStateReason_PLAYER_STATE_CHANGED)
	p.state.setActive(false)

	events := remoteEvents(p)
	require.Nil(t, events[len(events)-1])
	require.Nil(t, p.apiRemote())
}

// With metadata.enabled off the daemon makes no request playback does not
// need: the remote is announced, and its track stays unnamed.
func TestRemoteTrackStaysUnnamedWithoutMetadata(t *testing.T) {
	p := newRemoteTestPlayer(t, nil)

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, 1000, 0))))

	require.Len(t, remoteEvents(p), 1)
	require.Equal(t, otherDevice, p.apiRemote().DeviceId)
	require.Nil(t, p.apiRemote().Track)
	select {
	case <-p.remoteMeta:
		t.Fatal("metadata fetched with metadata.enabled off")
	default:
	}
}

// The answer to a push is the only cluster a device that just connected gets
// before something changes.
func TestPushAnswerShowsTheRemote(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))

	p.applyStatePushResult(statePushResult{cluster: remoteCluster(0x01, false, 1000, 0)})

	require.Equal(t, trackUri(0x01), p.remote.trackUri)
}

// A push answer travels on another channel than the dealer's updates: one
// older than the last cluster seen, or answering a push made while this
// device was active, would put back a remote that has moved on.
func TestStalePushAnswersAreDropped(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x02, false, 2000, 0))))

	p.applyStatePushResult(statePushResult{cluster: remoteCluster(0x01, false, 1000, 0)})
	require.Equal(t, trackUri(0x02), p.remote.trackUri, "older than the dealer's")

	self := remoteCluster(0x03, false, 3000, 0)
	self.ActiveDeviceId = thisDevice
	p.applyStatePushResult(statePushResult{cluster: self, sentActive: true})
	require.Equal(t, trackUri(0x02), p.remote.trackUri, "answering a push made while active")
}

// A device that takes the playback over shows as the remote at once.
func TestTransferredAwayShowsTheRemote(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))
	p.state.setActive(true)

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, time.Now().UnixMilli()+1000, 0))))

	require.False(t, p.state.active)
	require.Equal(t, otherDevice, p.apiRemote().DeviceId)
}

// Taking the session over when it is already here asks Spotify nothing.
func TestTransferWhileActiveDoesNothing(t *testing.T) {
	p := newRemoteTestPlayer(t, fetchNamed("Sushi"))
	p.state.setActive(true)

	resp, err := p.handleApiRequest(ApiRequest{Type: ApiRequestTypeTransfer})
	require.NoError(t, err)
	require.Nil(t, resp)
}

// A transfer Spotify refuses is the client's to handle, not a daemon fault.
func TestTransferRefusalsAreTyped(t *testing.T) {
	require.ErrorIs(t, transferError(&spclient.StatusError{Op: "transfer", StatusCode: http.StatusNotFound}), ErrNotFound)
	require.ErrorIs(t, transferError(&spclient.StatusError{Op: "transfer", StatusCode: http.StatusForbidden}), ErrForbidden)

	other := transferError(&spclient.StatusError{Op: "transfer", StatusCode: http.StatusBadGateway})
	require.NotErrorIs(t, other, ErrNotFound)
	require.NotErrorIs(t, other, ErrForbidden)
	require.Nil(t, transferError(nil))
}

// A device that leaves, or another that takes over, keeps the player state's
// timestamp: an answer to an earlier push with that same timestamp is no newer
// and must not put the previous remote back.
func TestPushAnswerWithTheSameTimestampIsStale(t *testing.T) {
	p := newRemoteTestPlayer(t, nil)
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, 1000, 0))))
	gone := remoteCluster(0x01, false, 1000, 0)
	gone.ActiveDeviceId = ""
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, gone)))

	p.applyStatePushResult(statePushResult{cluster: remoteCluster(0x01, false, 1000, 0)})
	require.Nil(t, p.apiRemote(), "the device left")

	third := remoteCluster(0x01, false, 1000, 0)
	third.ActiveDeviceId = thirdDevice
	third.Device[thirdDevice] = &connectpb.DeviceInfo{Name: "speaker"}
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, third)))
	p.applyStatePushResult(statePushResult{cluster: remoteCluster(0x01, false, 1000, 0)})
	require.Equal(t, thirdDevice, p.apiRemote().DeviceId, "another device took over")
}

// Push answers stay out of the timestamp that decides when this device was
// taken over: each device stamps its state with its own clock, and an answer
// from a device whose clock runs ahead would keep the device that really
// holds the session from taking it back.
func TestPushAnswersDoNotMoveTheTakeOverGate(t *testing.T) {
	p := newRemoteTestPlayer(t, nil)
	p.applyStatePushResult(statePushResult{cluster: remoteCluster(0x01, false, 5000, 0)})
	require.Zero(t, p.state.lastClusterTimestamp)

	p.state.takeOver()
	other := remoteCluster(0x02, false, 4000, 0)
	other.ActiveDeviceId = thirdDevice
	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, other)))
	require.False(t, p.state.active, "the other device took the session back")
}

// The answer to a push made while this device was active names it: never a
// remote, even after it stopped being active.
func TestPushAnswerFromWhileActiveIsIgnoredWhileActive(t *testing.T) {
	p := newRemoteTestPlayer(t, nil)
	p.state.setActive(true)
	p.applyStatePushResult(statePushResult{cluster: remoteCluster(0x01, false, 1000, 0)})
	p.state.setActive(false)
	require.Nil(t, p.remote, "a cluster seen while active is not observed")
}

// One track, one metadata request: a pause, a seek or a buffering there while
// it is on the way, or after it could not be had, asks for nothing more, and
// the track stays named through them.
func TestRemoteTrackIsFetchedOncePerTrack(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	p := newRemoteTestPlayer(t, func(ctx context.Context, uri string) (*librespot.Media, error) {
		calls.Add(1)
		<-release
		return fetchNamed("Sushi")(ctx, uri)
	})

	for _, c := range []*connectpb.Cluster{
		remoteCluster(0x01, false, 1000, 0),
		remoteCluster(0x01, true, 2000, 500),
		remoteCluster(0x01, false, 3000, 500),
		remoteCluster(0x01, false, 4000, 9000),
	} {
		require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, c)))
	}
	close(release)
	applyFetched(t, p)
	require.EqualValues(t, 1, calls.Load())

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, true, 5000, 9500))))
	require.Equal(t, "Sushi", p.apiRemote().Track.Name, "still named after a pause")
	require.EqualValues(t, 1, calls.Load())
}

// A track the metadata cache already holds is named at once.
func TestRemoteTrackFromTheCacheIsNamedAtOnce(t *testing.T) {
	p := newRemoteTestPlayer(t, func(context.Context, string) (*librespot.Media, error) {
		t.Fatal("fetched a track the cache holds")
		return nil, nil
	})
	p.app.metaCache = newTrackMetaCache(trackMetaCacheLimit)
	p.app.metaCache.put(trackUri(0x01), librespot.NewMediaFromTrack(metaTrack(gid(0x01), "cached")))

	require.NoError(t, p.handleDealerMessage(remoteClusterMessage(t, remoteCluster(0x01, false, 1000, 0))))
	require.Equal(t, "cached", p.apiRemote().Track.Name)
}
