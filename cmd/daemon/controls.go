package main

import (
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate"
	"go-librespot/tracks"
	"math"
	"time"
)

func (p *AppPlayer) handlePlayerEvent(ev *player.Event) {
	switch ev.Type {
	case player.EventTypePlaying:
		p.state.player.IsPlaying = true
		p.state.player.IsPaused = false
		p.state.player.IsBuffering = false
		p.updateState()

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypePaused:
		p.state.player.IsPlaying = true
		p.state.player.IsPaused = true
		p.state.player.IsBuffering = false
		p.updateState()

		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePaused,
			Data: ApiEventDataPaused{
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})
	case player.EventTypeNotPlaying:
		p.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeNotPlaying,
			Data: ApiEventDataNotPlaying{
				Uri:        p.state.player.Track.Uri,
				PlayOrigin: p.state.playOrigin(),
			},
		})

		hasNextTrack, err := p.advanceNext(false)
		if err != nil {
			// TODO: move into stopped state
			log.WithError(err).Error("failed advancing to next track")
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
	case player.EventTypeStopped:
		// do nothing
	default:
		panic("unhandled player event")
	}
}

type skipToFunc func(*connectpb.ContextTrack) bool

func (p *AppPlayer) loadContext(ctx *connectpb.Context, skipTo skipToFunc, paused bool) error {
	ctxTracks, err := tracks.NewTrackListFromContext(p.sess.Spclient(), ctx)
	if err != nil {
		return fmt.Errorf("failed creating track list: %w", err)
	}

	p.state.player.IsPaused = paused

	p.state.player.ContextUri = ctx.Uri
	p.state.player.ContextUrl = ctx.Url
	p.state.player.ContextRestrictions = ctx.Restrictions

	if p.state.player.ContextMetadata == nil {
		p.state.player.ContextMetadata = map[string]string{}
	}
	for k, v := range ctx.Metadata {
		p.state.player.ContextMetadata[k] = v
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if skipTo == nil {
		// if shuffle is enabled, we'll start from a random track
		if err := ctxTracks.ToggleShuffle(p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}

		// seek to the first track
		if err := ctxTracks.TrySeek(func(_ *connectpb.ContextTrack) bool { return true }); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}
	} else {
		// seek to the given track
		if err := ctxTracks.TrySeek(skipTo); err != nil {
			return fmt.Errorf("failed seeking to track: %w", err)
		}

		// shuffle afterwards
		if err := ctxTracks.ToggleShuffle(p.state.player.Options.ShufflingContext); err != nil {
			return fmt.Errorf("failed shuffling context")
		}
	}

	p.state.tracks = ctxTracks
	p.state.player.Track = ctxTracks.CurrentTrack()
	p.state.player.PrevTracks = ctxTracks.PrevTracks()
	p.state.player.NextTracks = ctxTracks.NextTracks()
	p.state.player.Index = ctxTracks.Index()

	// load current track into stream
	if err := p.loadCurrentTrack(paused); err != nil {
		return fmt.Errorf("failed loading current track (load context): %w", err)
	}

	return nil
}

func (p *AppPlayer) loadCurrentTrack(paused bool) error {
	if p.stream != nil {
		p.stream.Stop()
		p.stream = nil
	}

	spotId := librespot.SpotifyIdFromUri(p.state.player.Track.Uri)
	if spotId.Type() != librespot.SpotifyIdTypeTrack && spotId.Type() != librespot.SpotifyIdTypeEpisode {
		return fmt.Errorf("unsupported spotify type: %s", spotId.Type())
	}

	trackPosition := p.state.trackPosition()
	log.Debugf("loading %s %s (paused: %t, position: %dms)", spotId.Type(), spotId.Uri(), paused, trackPosition)

	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = true
	p.state.player.IsPaused = paused
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeWillPlay,
		Data: ApiEventDataWillPlay{
			Uri:        spotId.Uri(),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	stream, err := p.player.NewStream(spotId, *p.app.cfg.Bitrate, trackPosition, paused)
	if err != nil {
		return fmt.Errorf("failed creating stream for %s: %w", spotId, err)
	}

	log.Infof("loaded track \"%s\" (uri: %s, paused: %t, position: %dms, duration: %dms)", stream.Media.Name(), spotId.Uri(), paused, trackPosition, stream.Media.Duration())

	p.state.player.Duration = int64(stream.Media.Duration())
	p.state.player.IsPlaying = true
	p.state.player.IsBuffering = false
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*NewApiResponseStatusTrack(stream.Media, p.prodInfo, trackPosition)),
	})

	p.stream = stream
	return nil
}

func (p *AppPlayer) setRepeatingContext(val bool) {
	if val == p.state.player.Options.RepeatingContext {
		return
	}

	p.state.player.Options.RepeatingContext = val
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeRepeatContext,
		Data: ApiEventDataRepeatContext{
			Value: val,
		},
	})
}

func (p *AppPlayer) setRepeatingTrack(val bool) {
	if val == p.state.player.Options.RepeatingTrack {
		return
	}

	p.state.player.Options.RepeatingTrack = val
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeRepeatTrack,
		Data: ApiEventDataRepeatTrack{
			Value: val,
		},
	})
}

func (p *AppPlayer) setShufflingContext(val bool) {
	if val == p.state.player.Options.ShufflingContext {
		return
	}

	if err := p.state.tracks.ToggleShuffle(val); err != nil {
		log.WithError(err).Errorf("failed toggling shuffle context (value: %t)", val)
		return
	}

	p.state.player.Options.ShufflingContext = val
	p.state.player.Track = p.state.tracks.CurrentTrack()
	p.state.player.PrevTracks = p.state.tracks.PrevTracks()
	p.state.player.NextTracks = p.state.tracks.NextTracks()
	p.state.player.Index = p.state.tracks.Index()
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeShuffleContext,
		Data: ApiEventDataShuffleContext{
			Value: val,
		},
	})
}

func (p *AppPlayer) play() error {
	if p.stream == nil {
		return fmt.Errorf("no stream")
	}

	// seek before play to ensure we are at the correct stream position
	if err := p.stream.SeekMs(p.state.trackPosition()); err != nil {
		return fmt.Errorf("failed seeking before play: %w", err)
	}

	p.stream.Play()

	streamPos := p.stream.PositionMs()
	log.Debugf("resume track at %dms", streamPos)

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.player.IsPaused = false
	p.updateState()
	return nil
}

func (p *AppPlayer) pause() error {
	if p.stream == nil {
		return fmt.Errorf("no stream")
	}

	streamPos := p.stream.PositionMs()
	log.Debugf("pause track at %dms", streamPos)

	p.stream.Pause()

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = streamPos
	p.state.player.IsPaused = true
	p.updateState()
	return nil
}

func (p *AppPlayer) seek(position int64) error {
	if p.stream == nil {
		return fmt.Errorf("no stream")
	}

	log.Debugf("seek track to %dms", position)
	if err := p.stream.SeekMs(position); err != nil {
		return err
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = position
	p.updateState()

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeSeek,
		Data: ApiEventDataSeek{
			Uri:        p.state.player.Track.Uri,
			Position:   int(position),
			Duration:   int(p.stream.Media.Duration()),
			PlayOrigin: p.state.playOrigin(),
		},
	})

	return nil
}

func (p *AppPlayer) skipPrev() error {
	if p.stream.PositionMs() > 3000 {
		return p.seek(0)
	}

	if p.state.tracks != nil {
		log.Debug("skip previous track")
		p.state.tracks.GoPrev()

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks()
		p.state.player.Index = p.state.tracks.Index()
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	// load current track into stream
	if err := p.loadCurrentTrack(p.state.player.IsPaused); err != nil {
		return fmt.Errorf("failed loading current track (skip prev): %w", err)
	}

	return nil
}

func (p *AppPlayer) skipNext() error {
	if p.state.tracks != nil {
		log.Debug("skip next track")
		p.state.tracks.GoNext()

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks()
		p.state.player.Index = p.state.tracks.Index()
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	// load current track into stream
	if err := p.loadCurrentTrack(p.state.player.IsPaused); err != nil {
		return fmt.Errorf("failed loading current track (skip next): %w", err)
	}

	return nil
}

func (p *AppPlayer) advanceNext(forceNext bool) (bool, error) {
	var uri string
	var hasNextTrack bool
	if p.state.tracks != nil {
		if !forceNext && p.state.player.Options.RepeatingTrack {
			hasNextTrack = true
			p.state.player.IsPaused = false
		} else {
			// try to get the next track
			hasNextTrack = p.state.tracks.GoNext()

			// if we could not get the next track we probably ended the context
			if !hasNextTrack && p.state.player.Options.RepeatingContext {
				hasNextTrack = p.state.tracks.GoStart()
			}

			p.state.player.IsPaused = !hasNextTrack
		}

		p.state.player.Track = p.state.tracks.CurrentTrack()
		p.state.player.PrevTracks = p.state.tracks.PrevTracks()
		p.state.player.NextTracks = p.state.tracks.NextTracks()
		p.state.player.Index = p.state.tracks.Index()

		uri = p.state.player.Track.Uri
	}

	p.state.player.Timestamp = time.Now().UnixMilli()
	p.state.player.PositionAsOfTimestamp = 0

	if !hasNextTrack {
		p.state.player.IsPlaying = false
		p.state.player.IsPaused = false
		p.state.player.IsBuffering = false
	}

	// load current track into stream
	if err := p.loadCurrentTrack(!hasNextTrack); errors.Is(err, librespot.ErrMediaRestricted) || errors.Is(err, librespot.ErrNoSupportedFormats) {
		log.WithError(err).Infof("skipping unplayable media: %s", uri)
		if forceNext {
			// we failed in finding another track to play, just stop
			return false, err
		}

		return p.advanceNext(true)
	} else if err != nil {
		return false, fmt.Errorf("failed loading current track (advance to %s): %w", uri, err)
	}

	return hasNextTrack, nil
}

func (p *AppPlayer) updateVolume(newVal uint32) {
	if newVal > player.MaxStateVolume {
		newVal = player.MaxStateVolume
	} else if newVal < 0 {
		newVal = 0
	}

	log.Debugf("update volume to %d/%d", newVal, player.MaxStateVolume)
	p.player.SetVolume(newVal)
	p.state.device.Volume = newVal

	if err := p.putConnectState(connectpb.PutStateReason_VOLUME_CHANGED); err != nil {
		log.WithError(err).Error("failed put state after volume change")
	}

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeVolume,
		Data: ApiEventDataVolume{
			Value: uint32(math.Ceil(float64(newVal**p.app.cfg.VolumeSteps) / player.MaxStateVolume)),
			Max:   *p.app.cfg.VolumeSteps,
		},
	})
}
