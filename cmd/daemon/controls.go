package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	playerpb "github.com/devgianlu/go-librespot/proto/spotify/player"
	"github.com/devgianlu/go-librespot/tracks"
	"google.golang.org/protobuf/proto"
)

func (p *AppPlayer) prefetchNext() {
	ctx := context.TODO()

	next := p.state.tracks.PeekNext(ctx)
	if next == nil {
		return
	}

	if next.Uri == "" {
		// It should be implemented some day (the ContextTrack has enough
		// information to infer the track Uri) but it's hard to reproduce this
		// issue.
		p.app.log.Warn("cannot prefetch next track because the uri field is empty")
		return
	}

	nextId, err := librespot.SpotifyIdFromUri(next.Uri)
	if err != nil {
		p.app.log.WithError(err).WithField("uri", next.Uri).Warn("failed parsing prefetch uri")
		return
	} else if p.secondaryStream != nil && p.secondaryStream.Is(*nextId) {
		return
	}

	p.app.log.WithField("uri", nextId.Uri()).Debugf("prefetching next %s", nextId.Type())

	p.secondaryStream, err = p.player.NewStream(ctx, p.app.client, *nextId, p.app.cfg.Bitrate, 0)
	if err != nil {
		p.app.log.WithError(err).WithField("uri", nextId.String()).Warnf("failed prefetching %s stream", nextId.Type())
		return
	}

	p.player.SetSecondaryStream(p.secondaryStream.Source)

	p.app.log.WithField("uri", nextId.Uri()).
		Infof("prefetched %s %s (duration: %dms)", nextId.Type(),
			strconv.QuoteToGraphic(p.secondaryStream.Media.Name()), p.secondaryStream.Media.Duration())
}

func (p *AppPlayer) schedulePrefetchNext() {
	if p.state.player.IsPaused || p.primaryStream == nil {
		p.prefetchTimer.Reset(time.Duration(math.MaxInt64))
		return
	}

	untilTrackEnd := time.Duration(p.primaryStream.Media.Duration()-int32(p.player.PositionMs())) * time.Millisecond
	untilTrackEnd -= 30 * time.Second
	if untilTrackEnd < 10*time.Second {
		p.prefetchTimer.Reset(time.Duration(math.MaxInt64))

		go p.prefetchNext()
	} else {
		p.prefetchTimer.Reset(untilTrackEnd)
		p.app.log.Tracef("scheduling prefetch in %.0fs", untilTrackEnd.Seconds())
	}
}

func (p *AppPlayer) handlePlayerEvent(ctx context.Context, ev *player.Event) {
	switch ev.Type {
	case player.EventTypePlay:
		p.state.player.IsPlaying = true
		p.state.setPaused(false)
		p.state.player.IsBuffering = false
		p.updateState(ctx)

		p.sess.Events().OnPlayerPlay(
			p.primaryStream,
			p.state.player.ContextUri,
			p.state.player.Options.ShufflingContext,
			p.state.player.PlayOrigin,
			p.state.tracks.CurrentTrack(),
			p.state.trackPosition(),
		)

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				Resume:     false,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypeResume:
		p.state.player.IsPlaying = true
		p.state.setPaused(false)
		p.state.player.IsBuffering = false
		p.updateState(ctx)

		p.sess.Events().OnPlayerResume(p.primaryStream, p.state.trackPosition())

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				Resume:     true,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypePause:
		p.state.player.IsPlaying = true
		p.state.setPaused(true)
		p.state.player.IsBuffering = false
		p.updateState(ctx)

		p.sess.Events().OnPlayerPause(
			p.primaryStream,
			p.state.player.ContextUri,
			p.state.player.Options.ShufflingContext,
			p.state.player.PlayOrigin,
			p.state.tracks.CurrentTrack(),
			p.state.trackPosition(),
		)

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePaused,
			Data: ApiEventDataPaused{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypeNotPlaying:
		p.sess.Events().OnPlayerEnd(p.primaryStream, p.state.trackPosition())

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeNotPlaying,
			Data: ApiEventDataNotPlaying{
				ContextUri: p.state.player.ContextUri,
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})

		hasNextTrack, err := p.advanceNext(context.TODO(), false, false)
		if err != nil {
			p.app.log.WithError(err).Error("failed advancing to next track")
		}

		// if no track to be played, just stop
		if !hasNextTrack {
			p.app.server.Emit(&ApiEvent{
				Type: ApiEventTypeStopped,
				Data: ApiEventDataStopped{
					PlayOrigin: p.state.playOrigin(),
				},
			})
		}
	case player.EventTypeStop:
		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeStopped,
			Data: ApiEventDataStopped{
				PlayOrigin: p.state.playOrigin(),
			},
		})
	default:
		panic("unhandled player event")
	}
}

type skipToFunc func(*connectpb.ContextTrack) bool

func (p *AppPlayer) loadContext(ctx context.Context, spotCtx *connectpb.Context, skipTo skipToFunc, paused, drop bool) error {
	ctxTracks, err := tracks.NewTrackListFromContext(ctx, p.app.log, p.sess.Spclient(), spotCtx)
	if err != nil {
		return fmt.Errorf("failed creating track list: %w", err)
	}

	p.state.setPaused(paused)

	sessionId := make([]byte, 16)
	_, _ = rand.Read(sessionId)
	p.state.player.SessionId = base64.StdEncoding.EncodeToString(sessionId)

	p.state.player.ContextUri = spotCtx.Uri
	p.state.player.ContextUrl = spotCtx.Url
	p.state.player.Restrictions = spotCtx.Restrictions
	p.state.player.ContextRestrictions = spotCtx.Restrictions

	if p.state.player.ContextMetadata == nil {
		p.state.player.ContextMetadata = map[string]string{}
	}
	for k, v := range spotCtx.Metadata {
		p.state.player.ContextMetadata[k] = v
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if skipTo == nil {
		// if shuffle is enabled, we'll start from a random track
		if err := ctxTracks.ToggleShuffle(ctx, p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}

		// seek to the first track
		if err := ctxTracks.TrySeek(ctx, func(_ *connectpb.ContextTrack) bool { return true }); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}
	} else {
		// seek to the given track
		if err := ctxTracks.TrySeek(ctx, skipTo); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		// shuffle afterwards
		if err := ctxTracks.ToggleShuffle(ctx, p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}
	}

	p.state.tracks = ctxTracks
	p.state.player.Track = ctxTracks.CurrentTrack()
	p.state.player.PrevTracks = ctxTracks.PrevTracks()
	p.state.player.NextTracks = ctxTracks.NextTracks(ctx)
	p.state.player.Index = ctxTracks.Index()

	// load current track into stream
	if err := p.loadCurrentTrack(ctx, paused, drop); err != nil {
		return fmt.Errorf("failed loading current track (load context): %w", err)
	}

	return nil
}

func (p *AppPlayer) loadCurrentTrack(ctx context.Context, paused, drop bool) error {
	if p.primaryStream != nil {
		p.sess.Events().OnPrimaryStreamUnload(p.primaryStream, p.player.PositionMs())

		p.primaryStream = nil
	}

	spotId, err := librespot.SpotifyIdFromUri(p.state.player.Track.Uri)
	if err != nil {
		return fmt.Errorf("failed parsing uri: %w", err)
	} else if spotId.Type() != librespot.SpotifyIdTypeTrack && spotId.Type() != librespot.SpotifyIdTypeEpisode {
		return fmt.Errorf("unsupported spotify type: %s", spotId.Type())
	}

	trackPosition := p.state.trackPosition()
	p.app.log.WithField("uri", spotId.Uri()).
		Debugf("loading %s (paused: %t, position: %dms)", spotId.Type(), paused, trackPosition)

	p.state.updateTimestamp()
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = true
	p.state.player.IsPaused = paused
	p.state.player.PlaybackSpeed = 0 // not progressing while buffering
	p.updateState(ctx)

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeWillPlay,
		Data: ApiEventDataWillPlay{
			ContextUri: p.state.player.ContextUri,
			Uri:        spotId.Uri(),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	var prefetched bool
	if p.secondaryStream != nil && p.secondaryStream.Is(*spotId) {
		p.primaryStream = p.secondaryStream
		p.secondaryStream = nil
		prefetched = true
	} else {
		p.secondaryStream = nil
		prefetched = false

		var err error
		p.primaryStream, err = p.player.NewStream(ctx, p.app.client, *spotId, p.app.cfg.Bitrate, trackPosition)
		if err != nil {
			return fmt.Errorf("failed creating stream for %s: %w", spotId, err)
		}
	}

	if err := p.player.SetPrimaryStream(p.primaryStream.Source, paused, drop); err != nil {
		return fmt.Errorf("failed setting stream for %s: %w", spotId, err)
	}

	p.sess.Events().PostPrimaryStreamLoad(p.primaryStream, paused)

	p.app.log.WithField("uri", spotId.Uri()).
		Infof("loaded %s %s (paused: %t, position: %dms, duration: %dms, prefetched: %t)", spotId.Type(),
			strconv.QuoteToGraphic(p.primaryStream.Media.Name()), paused, trackPosition, p.primaryStream.Media.Duration(),
			prefetched)

	p.state.updateTimestamp()
	p.state.player.PlaybackId = hex.EncodeToString(p.primaryStream.PlaybackId)
	p.state.player.Duration = int64(p.primaryStream.Media.Duration())
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = false
	p.state.setPaused(paused) // update IsPaused and PlaybackSpeed
	p.updateState(ctx)
	p.schedulePrefetchNext()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*NewApiResponseStatusTrack(p.primaryStream.Media, p.prodInfo, trackPosition)),
	})
	return nil
}

func (p *AppPlayer) setOptions(ctx context.Context, repeatingContext *bool, repeatingTrack *bool, shufflingContext *bool) {
	var requiresUpdate bool
	if repeatingContext != nil && *repeatingContext != p.state.player.Options.RepeatingContext {
		p.state.player.Options.RepeatingContext = *repeatingContext

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeRepeatContext,
			Data: ApiEventDataRepeatContext{
				Value: *repeatingContext,
			},
		})

		requiresUpdate = true
	}

	if repeatingTrack != nil && *repeatingTrack != p.state.player.Options.RepeatingTrack {
		p.state.player.Options.RepeatingTrack = *repeatingTrack

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeRepeatTrack,
			Data: ApiEventDataRepeatTrack{
				Value: *repeatingTrack,
			},
		})

		requiresUpdate = true
	}

	if p.state.tracks != nil && shufflingContext != nil && *shufflingContext != p.state.player.Options.ShufflingContext {
		if err := p.state.tracks.ToggleShuffle(ctx, *shufflingContext); err != nil {
			p.app.log.WithError(err).Errorf("failed toggling shuffle context (value: %t)", *shufflingContext)
			return
		}

		p.state.player.Options.ShufflingContext = *shufflingContext
		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx)
		p.state.player.Index = p.state.tracks.Index()

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeShuffleContext,
			Data: ApiEventDataShuffleContext{
				Value: *shufflingContext,
			},
		})

		requiresUpdate = true
	}

	if requiresUpdate {
		p.updateState(ctx)
	}
}

func (p *AppPlayer) addToQueue(ctx context.Context, track *connectpb.ContextTrack) {
	if p.state.tracks == nil {
		p.app.log.Warnf("cannot add to queue without a context")
		return
	}

	if track.Uid == "" {
		// The uid always seems unset, so we have to set one manually.
		p.state.queueID++
		track.Uid = fmt.Sprintf("q%d", p.state.queueID)
	}

	p.state.tracks.AddToQueue(track)
	p.state.player.PrevTracks = p.state.tracks.PrevTracks()
	p.state.player.NextTracks = p.state.tracks.NextTracks(ctx)
	p.updateState(ctx)
	p.schedulePrefetchNext()
}

func (p *AppPlayer) setQueue(ctx context.Context, prev []*connectpb.ContextTrack, next []*connectpb.ContextTrack) {
	if p.state.tracks == nil {
		p.app.log.Warnf("cannot set queue without a context")
		return
	}

	p.state.tracks.SetQueue(prev, next)
	p.state.player.PrevTracks = p.state.tracks.PrevTracks()
	p.state.player.NextTracks = p.state.tracks.NextTracks(ctx)
	p.updateState(ctx)
	p.schedulePrefetchNext()
}

func (p *AppPlayer) play(ctx context.Context) error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	// seek before play to ensure we are at the correct stream position
	seekPos := p.state.trackPosition()
	seekPos = max(0, min(seekPos, int64(p.primaryStream.Media.Duration())))
	if err := p.player.SeekMs(seekPos); err != nil {
		return fmt.Errorf("failed seeking before play: %w", err)
	}

	if err := p.player.Play(); err != nil {
		return fmt.Errorf("failed starting playback: %w", err)
	}

	streamPos := p.player.PositionMs()
	p.app.log.Debugf("resume track at %dms", streamPos)

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.setPaused(false)
	p.updateState(ctx)
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) pause(ctx context.Context) error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	streamPos := p.player.PositionMs()
	p.app.log.Debugf("pause track at %dms", streamPos)

	if err := p.player.Pause(); err != nil {
		return fmt.Errorf("failed pausing playback: %w", err)
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.setPaused(true)
	p.updateState(ctx)
	p.schedulePrefetchNext()

	return nil
}

func (p *AppPlayer) seek(ctx context.Context, position int64) error {
	if p.primaryStream == nil {
		return fmt.Errorf("no primary stream")
	}

	oldPosition := p.player.PositionMs()
	position = max(0, min(position, int64(p.primaryStream.Media.Duration())))

	p.app.log.Debugf("seek track to %dms", position)
	if err := p.player.SeekMs(position); err != nil {
		return err
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = position
	p.updateState(ctx)
	p.schedulePrefetchNext()

	p.sess.Events().OnPlayerSeek(p.primaryStream, oldPosition, position)

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeSeek,
		Data: ApiEventDataSeek{
			ContextUri: p.state.player.ContextUri,
			Uri:        p.state.player.Track.Uri,
			Position:   int(position),
			Duration:   int(p.primaryStream.Media.Duration()),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	return nil
}

func (p *AppPlayer) skipPrev(ctx context.Context, allowSeeking bool) error {
	if allowSeeking && p.player.PositionMs() > 3000 {
		return p.seek(ctx, 0)
	}

	p.sess.Events().OnPlayerSkipBackward(p.primaryStream, p.player.PositionMs())

	if p.state.tracks != nil {
		p.app.log.Debug("skip previous track")
		p.state.tracks.GoPrev()

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx)
		p.state.player.Index = p.state.tracks.Index()
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	// load current track into stream
	if err := p.loadCurrentTrack(ctx, p.state.player.IsPaused, true); err != nil {
		return fmt.Errorf("failed loading current track (skip prev): %w", err)
	}

	return nil
}

func (p *AppPlayer) skipNext(ctx context.Context, track *connectpb.ContextTrack) error {
	p.sess.Events().OnPlayerSkipForward(p.primaryStream, p.player.PositionMs(), track != nil)

	if track != nil {
		contextSpotType := librespot.InferSpotifyIdTypeFromContextUri(p.state.player.ContextUri)
		if err := p.state.tracks.TrySeek(ctx, tracks.ContextTrackComparator(contextSpotType, track)); err != nil {
			return err
		}

		p.state.player.Timestamp = time.Now().UnixMilli()
		p.state.player.PositionAsOfTimestamp = 0

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx)
		p.state.player.Index = p.state.tracks.Index()

		if err := p.loadCurrentTrack(ctx, p.state.player.IsPaused, true); err != nil {
			return err
		}
		return nil
	} else {
		hasNextTrack, err := p.advanceNext(ctx, true, true)
		if err != nil {
			return fmt.Errorf("failed skipping to next track: %w", err)
		}

		// if no track to be played, just stop
		if !hasNextTrack {
			p.app.server.Emit(&ApiEvent{
				Type: ApiEventTypeStopped,
				Data: ApiEventDataStopped{
					PlayOrigin: p.state.playOrigin(),
				},
			})
		}

		return nil
	}
}

func (p *AppPlayer) advanceNext(ctx context.Context, forceNext, drop bool) (bool, error) {
	var uri string
	var hasNextTrack bool
	if p.state.tracks != nil {
		if !forceNext && p.state.player.Options.RepeatingTrack {
			hasNextTrack = true
			p.state.player.IsPaused = false
		} else {
			// try to get the next track
			hasNextTrack = p.state.tracks.GoNext(ctx)

			// if we could not get the next track we probably ended the context
			if !hasNextTrack {
				hasNextTrack = p.state.tracks.GoStart(ctx)

				// if repeating is disabled move to the first track, but do not start it
				if !p.state.player.Options.RepeatingContext {
					hasNextTrack = false
				}
			}

			p.state.player.IsPaused = !hasNextTrack
		}

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks(ctx)
		p.state.player.Index = p.state.tracks.Index()

		uri = p.state.player.Track.Uri
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if !hasNextTrack && !p.app.cfg.DisableAutoplay {
		p.state.player.Suppressions = &connectpb.Suppressions{}

		// Consider all tracks as recent because we got here by reaching the end of the context
		var prevTrackUris []string
		for _, track := range p.state.tracks.AllTracks(ctx) {
			prevTrackUris = append(prevTrackUris, track.Uri)
		}

		p.app.log.Debugf("resolving autoplay station for %d tracks", len(prevTrackUris))
		spotCtx, err := p.sess.Spclient().ContextResolveAutoplay(ctx, &playerpb.AutoplayContextRequest{
			ContextUri:     proto.String(p.state.player.ContextUri),
			RecentTrackUri: prevTrackUris,
		})
		if err != nil {
			p.app.log.WithError(err).Warnf("failed resolving station for %s", p.state.player.ContextUri)
			return false, nil
		}

		p.app.log.Debugf("resolved autoplay station: %s", spotCtx.Uri)
		if err := p.loadContext(ctx, spotCtx, func(_ *connectpb.ContextTrack) bool { return true }, false, drop); err != nil {
			p.app.log.WithError(err).Warnf("failed loading station for %s", p.state.player.ContextUri)
			return false, nil
		}

		return true, nil
	}

	if !hasNextTrack {
		p.state.player.IsPlaying = false
		p.state.player.IsPaused = false
		p.state.player.IsBuffering = false
	}

	// load current track into stream
	if err := p.loadCurrentTrack(ctx, !hasNextTrack, drop); errors.Is(err, librespot.ErrMediaRestricted) || errors.Is(err, librespot.ErrNoSupportedFormats) {
		p.app.log.WithError(err).Infof("skipping unplayable media: %s", uri)
		if forceNext {
			// we failed in finding another track to play, just stop
			return false, err
		}

		return p.advanceNext(ctx, true, drop)
	} else if err != nil {
		return false, fmt.Errorf("failed loading current track (advance to %s): %w", uri, err)
	}

	return hasNextTrack, nil
}

// Return the volume as an integer in the range 0..player.MaxStateVolume, as
// used in the API.
func (p *AppPlayer) apiVolume() uint32 {
	return uint32(math.Round(float64(p.state.device.Volume*p.app.cfg.VolumeSteps) / player.MaxStateVolume))
}

// Set the player volume to the new volume, also notifies about the change.
func (p *AppPlayer) updateVolume(newVal uint32) {
	if newVal > player.MaxStateVolume {
		newVal = player.MaxStateVolume
	} else if newVal < 0 {
		newVal = 0
	}

	p.app.log.Debugf("update volume requested to %d/%d", newVal, player.MaxStateVolume)
	p.player.SetVolume(newVal)

	// If there is a value in the channel buffer, remove it.
	select {
	case <-p.volumeUpdate:
	default:
	}

	p.volumeUpdate <- float32(newVal) / player.MaxStateVolume
}

// Send notification that the volume changed.
// The original change can come from anywhere: from Spotify Connect, from the
// REST API, or from a volume mixer.
func (p *AppPlayer) volumeUpdated(ctx context.Context) {
	if err := p.putConnectState(ctx, connectpb.PutStateReason_VOLUME_CHANGED); err != nil {
		p.app.log.WithError(err).Error("failed put state after volume change")
	}

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeVolume,
		Data: ApiEventDataVolume{
			Value: p.apiVolume(),
			Max:   p.app.cfg.VolumeSteps,
		},
	})
}
