package daemon

import (
	"bytes"
	"context"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
)

// Spotify keeps podcast progress server-side, in what the desktop client calls
// the resumption platform. A play command from a controller carries no start
// position — only a transfer does — so an episode left half-listened on
// another device restarts from zero unless the device looks its resume point
// up itself. Progress made here is written back so the other devices see it.
//
// Only episodes have resume points; everything here is a no-op for tracks.

// resumeTimeout bounds a resume point lookup or report. These run on the
// player's own goroutine, so a stalled backend must not hold up playback: the
// feature is best-effort and degrades to "start from the beginning".
const resumeTimeout = 10 * time.Second

// resumeCurrentEpisode moves the pending start position of the current track
// to its server-side resume point, if it is a partially listened episode.
//
// Call it where playback is about to start a track from the beginning. A
// transferred position is authoritative — the device handing over already
// applied the resume point — so the transfer path deliberately does not.
//
// Failing to reach the resumption service is logged and otherwise ignored: an
// episode starting from the beginning is much better than one that won't play.
func (p *AppPlayer) resumeCurrentEpisode(ctx context.Context) {
	if p.state.player.Track == nil {
		return
	}

	id, err := librespot.SpotifyIdFromUri(p.state.player.Track.Uri)
	if err != nil || id.Type() != librespot.SpotifyIdTypeEpisode {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
	defer cancel()

	positionMs, err := p.sess.Spclient().ResumePositionMs(ctx, *id)
	if err != nil {
		p.app.log.WithError(err).WithField("uri", id.Uri()).Warn("failed getting episode resume point")
		return
	}

	if positionMs <= 0 {
		return
	}

	p.app.log.WithField("uri", id.Uri()).Debugf("resuming episode at %dms", positionMs)
	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = positionMs
}

// reportResumePosition stores how far into an episode playback has got.
func (p *AppPlayer) reportResumePosition(ctx context.Context, stream *player.Stream, positionMs int64) {
	if stream == nil || stream.RequestedId.Type() != librespot.SpotifyIdTypeEpisode || positionMs <= 0 {
		return
	}

	// An episode that just played out is unloaded right after being reported
	// as finished. Reporting its position now would replace that with a
	// position a second short of the end, and the episode would "resume"
	// there forever instead of starting over.
	if bytes.Equal(stream.PlaybackId, p.resumeFinishedPlaybackId) {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
	defer cancel()

	if err := p.sess.Spclient().SetResumePositionMs(ctx, stream.RequestedId, positionMs); err != nil {
		p.app.log.WithError(err).WithField("uri", stream.RequestedId.Uri()).
			Warn("failed reporting episode resume point")
		return
	}

	p.app.log.WithField("uri", stream.RequestedId.Uri()).Debugf("reported episode position %dms", positionMs)
}

// reportResumeFinished marks an episode as listened to the end, so that
// playing it again starts it over instead of resuming near the end.
func (p *AppPlayer) reportResumeFinished(ctx context.Context, stream *player.Stream) {
	if stream == nil || stream.RequestedId.Type() != librespot.SpotifyIdTypeEpisode {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, resumeTimeout)
	defer cancel()

	// Recorded even if the report fails: either way this stream reached its
	// end, and reporting a near-end position for it would be wrong.
	p.resumeFinishedPlaybackId = stream.PlaybackId

	if err := p.sess.Spclient().SetResumeFinished(ctx, stream.RequestedId); err != nil {
		p.app.log.WithError(err).WithField("uri", stream.RequestedId.Uri()).
			Warn("failed reporting episode as finished")
		return
	}

	p.app.log.WithField("uri", stream.RequestedId.Uri()).Debug("reported episode as finished")
}
