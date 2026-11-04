package daemon

import (
	"context"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

// remotePlayback is what the account plays on another Connect device, read
// from the cluster Spotify pushes to every device of the account. The position
// is an anchor: positionAsOf at timestamp, moving while playing.
type remotePlayback struct {
	deviceId   string
	deviceName string
	deviceType string

	trackUri     string
	playing      bool
	paused       bool
	buffering    bool
	speed        float64
	timestamp    int64
	positionAsOf int64

	// media is the track's metadata, nil until resolved: the cluster carries
	// a title and an album but no artist name.
	media *librespot.Media
}

// transferTimeout bounds the request taking the session over from the active
// device.
const transferTimeout = 30 * time.Second

// transferError carries a transfer Spotify refused over as the library
// requests carry theirs (libraryError): a 403 or a 404 is the client's to
// handle.
func transferError(err error) error {
	if err == nil {
		return nil
	}
	return libraryError("transfer", err)
}

type remoteMetaResult struct {
	uri   string
	media *librespot.Media
}

// remoteFromCluster reads the remote playback out of a cluster. Nil when no
// device is active, when this one is, or when nothing is loaded: with no
// active device the cluster still carries the last player state, which
// describes nothing playing anywhere. An active id the cluster lists no device
// for counts as no active device, as it does for the Spotify clients.
func remoteFromCluster(c *connectpb.Cluster, self string) *remotePlayback {
	if c == nil || c.ActiveDeviceId == "" || c.ActiveDeviceId == self {
		return nil
	}

	device := c.Device[c.ActiveDeviceId]
	if device == nil {
		return nil
	}

	ps := c.PlayerState
	if ps == nil || ps.Track == nil || ps.Track.Uri == "" {
		return nil
	}

	return &remotePlayback{
		deviceId:     c.ActiveDeviceId,
		deviceName:   device.Name,
		deviceType:   device.DeviceType.String(),
		trackUri:     ps.Track.Uri,
		playing:      ps.IsPlaying,
		paused:       ps.IsPaused,
		buffering:    ps.IsBuffering,
		speed:        ps.PlaybackSpeed,
		timestamp:    ps.Timestamp,
		positionAsOf: ps.PositionAsOfTimestamp,
	}
}

// sameAs reports whether two remote playbacks describe the same thing. The
// cluster is re-sent on changes that are not about playback (a device
// connecting, this device's own pushes), so most updates change nothing here.
func (r *remotePlayback) sameAs(o *remotePlayback) bool {
	if r == nil || o == nil {
		return r == o
	}
	return r.deviceId == o.deviceId && r.deviceName == o.deviceName && r.deviceType == o.deviceType &&
		r.trackUri == o.trackUri && r.playing == o.playing && r.paused == o.paused &&
		r.buffering == o.buffering && r.speed == o.speed &&
		r.timestamp == o.timestamp && r.positionAsOf == o.positionAsOf
}

// moving reports whether the remote playhead advances: playing, neither
// paused nor buffering.
func (r *remotePlayback) moving() bool {
	return r.playing && !r.paused && !r.buffering
}

// position is where the remote playhead is now, advancing at the playback
// speed (a podcast played faster), or at 1 where the state gives none.
func (r *remotePlayback) position(now int64) int64 {
	pos := r.positionAsOf
	if r.moving() && now > r.timestamp {
		speed := r.speed
		if speed <= 0 {
			speed = 1
		}
		pos += int64(float64(now-r.timestamp) * speed)
	}
	if r.media != nil {
		pos = min(pos, int64(r.media.Duration()))
	}
	return max(pos, 0)
}

// observeCluster takes in a cluster seen while this device is not active, and
// raises the remote event when what plays elsewhere changed. Runs on the Run
// goroutine.
func (p *AppPlayer) observeCluster(c *connectpb.Cluster) {
	next := remoteFromCluster(c, p.app.deviceId)
	sameTrack := next != nil && p.remote != nil && next.trackUri == p.remote.trackUri
	if sameTrack {
		next.media = p.remote.media
	}
	if next.sameAs(p.remote) {
		return
	}

	p.remote = next
	// Once per track: a pause or a seek there while its metadata is on the way,
	// or after it could not be had, asks for nothing more.
	if next != nil && !sameTrack {
		p.resolveRemoteTrack(next.trackUri)
	}

	p.app.server.Emit(&ApiEvent{Type: ApiEventTypeRemote, Data: p.apiRemote()})
}

// forgetRemoteIfActive drops the remote playback once this device is the
// active one, raising the remote event with null data: what played elsewhere
// plays here now, and a later local stop must not bring the old record back.
// Every way of becoming active pushes the state, which calls this. Runs on the
// Run goroutine.
func (p *AppPlayer) forgetRemoteIfActive() {
	if !p.state.active || p.remote == nil {
		return
	}

	p.remote = nil
	p.app.server.Emit(&ApiEvent{Type: ApiEventTypeRemote, Data: (*ApiRemote)(nil)})
}

// resolveRemoteTrack fetches the remote track's metadata off the loop, from
// the metadata cache when it holds it. With metadata.enabled off the daemon
// makes no request playback does not need, so the track stays unnamed.
func (p *AppPlayer) resolveRemoteTrack(uri string) {
	if media := p.app.metaCache.get(uri); media != nil {
		p.remote.media = media
		return
	}

	if _, ok := metaExtensionKind(uri); !ok || p.fetchRemoteMedia == nil {
		return
	}

	p.goDetached(metaFetchTimeout, func(ctx context.Context) {
		media, err := p.fetchRemoteMedia(ctx, uri)
		if err != nil {
			p.app.log.WithError(err).Warnf("failed resolving metadata of remote track %s", uri)
			return
		}

		select {
		case p.remoteMeta <- remoteMetaResult{uri: uri, media: media}:
		case <-ctx.Done():
		}
	})
}

// applyRemoteMeta attaches resolved metadata to the remote playback, unless
// the remote has moved on to another track meanwhile. Runs on the Run
// goroutine.
func (p *AppPlayer) applyRemoteMeta(res remoteMetaResult) {
	if p.remote == nil || p.remote.trackUri != res.uri {
		return
	}

	p.remote.media = res.media
	p.app.server.Emit(&ApiEvent{Type: ApiEventTypeRemote, Data: p.apiRemote()})
}

// apiRemote describes the remote playback for the API. Nil while this device
// is active: a cluster naming another device that arrives after this one took
// over is stale, and the next push drops it.
func (p *AppPlayer) apiRemote() *ApiRemote {
	r := p.remote
	if r == nil || p.state.active {
		return nil
	}

	resp := &ApiRemote{
		DeviceId:   r.deviceId,
		DeviceName: r.deviceName,
		DeviceType: r.deviceType,
		// A client extrapolates the position unless paused: a remote that
		// does not advance (stopped, buffering) is reported as one.
		Paused: !r.moving(),
	}
	if r.media != nil && p.prodInfo != nil {
		resp.Track = p.newApiResponseStatusMedia(r.media, r.position(time.Now().UnixMilli()))
	}
	return resp
}
