package main

import (
	"errors"
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"time"
)

func (s *Session) handlePlayerEvent(ev *player.Event) {
	switch ev.Type {
	case player.EventTypePlaying:
		s.state.playerState.IsPlaying = true
		s.state.playerState.IsPaused = false
		s.state.playerState.IsBuffering = false
		s.updateState()

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				Uri:        s.state.playerState.Track.Uri,
				PlayOrigin: s.state.playOrigin(),
			},
		})
	case player.EventTypePaused:
		s.state.playerState.IsPlaying = true
		s.state.playerState.IsPaused = true
		s.state.playerState.IsBuffering = false
		s.updateState()

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePaused,
			Data: ApiEventDataPaused{
				Uri:        s.state.playerState.Track.Uri,
				PlayOrigin: s.state.playOrigin(),
			},
		})
	case player.EventTypeNotPlaying:
		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeNotPlaying,
			Data: ApiEventDataNotPlaying{
				Uri:        s.state.playerState.Track.Uri,
				PlayOrigin: s.state.playOrigin(),
			},
		})

		hasNextTrack, err := s.advanceNext(false)
		if err != nil {
			// TODO: move into stopped state
			log.WithError(err).Error("failed advancing to next track")
		}

		// if no track to be played, just stop
		if !hasNextTrack {
			s.app.server.Emit(&ApiEvent{
				Type: ApiEventTypeStopped,
				Data: ApiEventDataStopped{
					PlayOrigin: s.state.playOrigin(),
				},
			})
		}
	case player.EventTypeStopped:
		// do nothing
	default:
		panic("unhandled player event")
	}
}

func (s *Session) loadContext(ctx *connectpb.Context, skipTo func(*connectpb.ContextTrack) bool, paused bool) error {
	tracks, err := NewTrackListFromContext(s.sp, ctx)
	if err != nil {
		return fmt.Errorf("failed creating track list: %w", err)
	}

	s.state.playerState.IsPaused = paused

	s.state.playerState.ContextUri = ctx.Uri
	s.state.playerState.ContextUrl = ctx.Url
	s.state.playerState.ContextRestrictions = ctx.Restrictions

	if s.state.playerState.ContextMetadata == nil {
		s.state.playerState.ContextMetadata = map[string]string{}
	}
	for k, v := range ctx.Metadata {
		s.state.playerState.ContextMetadata[k] = v
	}

	s.state.playerState.Timestamp = time.Now().UnixMilli()
	s.state.playerState.PositionAsOfTimestamp = 0

	// if we fail to seek, just fallback to the first track
	tracks.TrySeek(skipTo)

	s.state.tracks = tracks
	s.state.playerState.Track = tracks.CurrentTrack()
	s.state.playerState.PrevTracks = tracks.PrevTracks()
	s.state.playerState.NextTracks = tracks.NextTracks()
	s.state.playerState.Index = tracks.Index()

	// load current track into stream
	if err := s.loadCurrentTrack(paused); err != nil {
		return fmt.Errorf("failed loading current track (load context): %w", err)
	}

	return nil
}

func (s *Session) loadCurrentTrack(paused bool) error {
	if s.stream != nil {
		s.stream.Stop()
		s.stream = nil
	}

	trackId := librespot.TrackIdFromUri(s.state.playerState.Track.Uri)
	trackPosition := s.state.trackPosition()

	log.Debugf("loading track %s (paused: %t, position: %dms)", trackId.Uri(), paused, trackPosition)

	s.state.playerState.IsPlaying = true
	s.state.playerState.IsBuffering = true
	s.state.playerState.IsPaused = paused
	s.updateState()

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeWillPlay,
		Data: ApiEventDataWillPlay{
			Uri:        trackId.Uri(),
			PlayOrigin: s.state.playOrigin(),
		},
	})

	stream, err := s.player.NewStream(trackId, s.app.cfg.Bitrate, trackPosition, paused)
	if err != nil {
		return fmt.Errorf("failed creating stream: %w", err)
	}

	log.Infof("loaded track \"%s\" (uri: %s, paused: %t, position: %dms, duration: %dms)", *stream.Track.Name, trackId.Uri(), paused, trackPosition, *stream.Track.Duration)

	s.state.playerState.Duration = int64(*stream.Track.Duration)
	s.state.playerState.IsPlaying = true
	s.state.playerState.IsBuffering = false
	s.updateState()

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*NewApiResponseStatusTrack(stream.Track, s.prodInfo, trackPosition)),
	})

	s.stream = stream
	return nil
}

func (s *Session) play() error {
	if s.stream == nil {
		return fmt.Errorf("no stream")
	}

	s.stream.Play()

	streamPos := s.stream.PositionMs()
	log.Debugf("resume track at %dms", streamPos)

	s.state.playerState.Timestamp = time.Now().UnixMilli()
	s.state.playerState.PositionAsOfTimestamp = streamPos
	s.state.playerState.IsPaused = false
	s.updateState()
	return nil
}

func (s *Session) pause() error {
	if s.stream == nil {
		return fmt.Errorf("no stream")
	}

	streamPos := s.stream.PositionMs()
	log.Debugf("pause track at %dms", streamPos)

	s.stream.Pause()

	s.state.playerState.Timestamp = time.Now().UnixMilli()
	s.state.playerState.PositionAsOfTimestamp = streamPos
	s.state.playerState.IsPaused = true
	s.updateState()
	return nil
}

func (s *Session) seek(position int64) error {
	if s.stream == nil {
		return fmt.Errorf("no stream")
	}

	log.Debugf("seek track to %dms", position)
	if err := s.stream.SeekMs(position); err != nil {
		return err
	}

	s.state.playerState.Timestamp = time.Now().UnixMilli()
	s.state.playerState.PositionAsOfTimestamp = position
	s.updateState()

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeSeek,
		Data: ApiEventDataSeek{
			Uri:        s.state.playerState.Track.Uri,
			Position:   int(position),
			Duration:   int(*s.stream.Track.Duration),
			PlayOrigin: s.state.playOrigin(),
		},
	})

	return nil
}

func (s *Session) skipPrev() error {
	if s.stream.PositionMs() > 3000 {
		return s.seek(0)
	}

	if s.state.tracks != nil {
		log.Debug("skip previous track")
		s.state.tracks.GoPrev()

		s.state.playerState.Track = s.state.tracks.CurrentTrack()
		s.state.playerState.PrevTracks = s.state.tracks.PrevTracks()
		s.state.playerState.NextTracks = s.state.tracks.NextTracks()
		s.state.playerState.Index = s.state.tracks.Index()
	}

	s.state.playerState.Timestamp = time.Now().UnixMilli()
	s.state.playerState.PositionAsOfTimestamp = 0

	// load current track into stream
	if err := s.loadCurrentTrack(s.state.playerState.IsPaused); err != nil {
		return fmt.Errorf("failed loading current track (skip prev): %w", err)
	}

	return nil
}

func (s *Session) skipNext() error {
	if s.state.tracks != nil {
		log.Debug("skip next track")
		s.state.tracks.GoNext()

		s.state.playerState.Track = s.state.tracks.CurrentTrack()
		s.state.playerState.PrevTracks = s.state.tracks.PrevTracks()
		s.state.playerState.NextTracks = s.state.tracks.NextTracks()
		s.state.playerState.Index = s.state.tracks.Index()
	}

	s.state.playerState.Timestamp = time.Now().UnixMilli()
	s.state.playerState.PositionAsOfTimestamp = 0

	// load current track into stream
	if err := s.loadCurrentTrack(s.state.playerState.IsPaused); err != nil {
		return fmt.Errorf("failed loading current track (skip next): %w", err)
	}

	return nil
}

func (s *Session) advanceNext(forceNext bool) (bool, error) {
	var uri string
	var hasNextTrack bool
	if s.state.tracks != nil {
		if !forceNext && s.state.playerState.Options.RepeatingTrack {
			hasNextTrack = true
			s.state.playerState.IsPaused = false
		} else {
			// try to get the next track
			hasNextTrack = s.state.tracks.GoNext()

			// if we could not get the next track we probably ended the context
			if !hasNextTrack && s.state.playerState.Options.RepeatingContext {
				hasNextTrack = s.state.tracks.GoStart()
			}

			s.state.playerState.IsPaused = !hasNextTrack
		}

		s.state.playerState.Track = s.state.tracks.CurrentTrack()
		s.state.playerState.PrevTracks = s.state.tracks.PrevTracks()
		s.state.playerState.NextTracks = s.state.tracks.NextTracks()
		s.state.playerState.Index = s.state.tracks.Index()

		uri = s.state.playerState.Track.Uri
	}

	s.state.playerState.Timestamp = time.Now().UnixMilli()
	s.state.playerState.PositionAsOfTimestamp = 0

	if !hasNextTrack {
		s.state.playerState.IsPlaying = false
		s.state.playerState.IsPaused = false
		s.state.playerState.IsBuffering = false
	}

	// load current track into stream
	if err := s.loadCurrentTrack(!hasNextTrack); errors.Is(err, librespot.ErrTrackRestricted) {
		log.Infof("skipping restricted track: %s", uri)
		if forceNext {
			// we failed in finding another track to play, just stop
			return false, err
		}

		return s.advanceNext(true)
	} else if err != nil {
		return false, fmt.Errorf("failed loading current track (advance to %s): %w", uri, err)
	}

	return hasNextTrack, nil
}

func (s *Session) updateVolume(newVal uint32) {
	if newVal > player.MaxStateVolume {
		newVal = player.MaxStateVolume
	} else if newVal < 0 {
		newVal = 0
	}

	log.Debugf("update volume to %d/%d", newVal, player.MaxStateVolume)
	s.player.SetVolume(newVal)
	s.state.deviceInfo.Volume = newVal

	if err := s.putConnectState(connectpb.PutStateReason_VOLUME_CHANGED); err != nil {
		log.WithError(err).Error("failed put state after volume change")
	}

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeVolume,
		Data: ApiEventDataVolume{
			Value: newVal * s.app.cfg.VolumeSteps / player.MaxStateVolume,
			Max:   s.app.cfg.VolumeSteps,
		},
	})
}
