package main

import (
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
		var uri, playOrigin string
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = false
			s.playerState.IsBuffering = false

			uri = s.playerState.Track.Uri
			playOrigin = s.playerState.PlayOrigin.FeatureIdentifier
		})

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
			Data: ApiEventDataPlaying{
				Uri:        uri,
				PlayOrigin: playOrigin,
			},
		})
	case player.EventTypePaused:
		var uri, playOrigin string
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = true
			s.playerState.IsBuffering = false

			uri = s.playerState.Track.Uri
			playOrigin = s.playerState.PlayOrigin.FeatureIdentifier
		})

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePaused,
			Data: ApiEventDataPaused{
				Uri:        uri,
				PlayOrigin: playOrigin,
			},
		})
	case player.EventTypeNotPlaying:
		var prevUri, playOrigin string
		var hasNextTrack bool
		s.withState(func(s *State) {
			prevUri = s.playerState.Track.Uri

			if s.tracks != nil {
				if s.playerState.Options.RepeatingTrack {
					hasNextTrack = true
					s.playerState.IsPaused = false
				} else {
					hasNextTrack = s.tracks.GoNext()
					s.playerState.IsPaused = !hasNextTrack

					s.playerState.Track = s.tracks.CurrentTrack()
					s.playerState.PrevTracks = s.tracks.PrevTracks()
					s.playerState.NextTracks = s.tracks.NextTracks()
					s.playerState.Index = s.tracks.Index()
				}
			}

			s.playerState.Timestamp = time.Now().UnixMilli()
			s.playerState.PositionAsOfTimestamp = 0

			playOrigin = s.playerState.PlayOrigin.FeatureIdentifier

			if !hasNextTrack {
				s.playerState.IsPlaying = false
				s.playerState.IsPaused = false
				s.playerState.IsBuffering = false
			}
		})

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypeNotPlaying,
			Data: ApiEventDataNotPlaying{
				Uri:        prevUri,
				PlayOrigin: playOrigin,
			},
		})

		// load current track into stream
		if err := s.loadCurrentTrack(); err != nil {
			log.WithError(err).Error("failed loading current track")
			return
		}

		// start playing if there is something next or become stopped
		if hasNextTrack {
			if err := s.play(); err != nil {
				log.WithError(err).Error("failed playing")
				return
			}
		} else {
			s.app.server.Emit(&ApiEvent{
				Type: ApiEventTypeStopped,
				Data: ApiEventDataStopped{
					PlayOrigin: playOrigin,
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

	s.withState(func(s *State) {
		s.playerState.IsPaused = paused

		s.playerState.ContextUri = ctx.Uri
		s.playerState.ContextUrl = ctx.Url
		s.playerState.ContextRestrictions = ctx.Restrictions

		if s.playerState.ContextMetadata == nil {
			s.playerState.ContextMetadata = map[string]string{}
		}
		for k, v := range ctx.Metadata {
			s.playerState.ContextMetadata[k] = v
		}

		s.playerState.Timestamp = time.Now().UnixMilli()
		s.playerState.PositionAsOfTimestamp = 0
	})

	// if we fail to seek, just fallback to the first track
	tracks.TrySeek(skipTo)

	s.withState(func(s *State) {
		s.tracks = tracks
		s.playerState.Track = tracks.CurrentTrack()
		s.playerState.PrevTracks = tracks.PrevTracks()
		s.playerState.NextTracks = tracks.NextTracks()
		s.playerState.Index = tracks.Index()
	})

	// load current track into stream
	if err := s.loadCurrentTrack(); err != nil {
		return fmt.Errorf("failed loading current track: %w", err)
	}

	// start playing if not initially paused
	if !paused {
		if err := s.play(); err != nil {
			return fmt.Errorf("failed playing: %w", err)
		}
	}

	return nil
}

func (s *Session) loadCurrentTrack() error {
	if s.stream != nil {
		s.stream.Stop()
		s.stream = nil
	}

	var playOrigin string
	var trackPosition int64
	var trackId librespot.TrackId
	s.updateState(func(s *State) {
		playOrigin = s.playerState.PlayOrigin.FeatureIdentifier
		trackId = librespot.TrackIdFromUri(s.playerState.Track.Uri)
		trackPosition = s.trackPosition()

		s.playerState.IsPlaying = true
		s.playerState.IsBuffering = true
	})

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeWillPlay,
		Data: ApiEventDataWillPlay{
			Uri:        trackId.Uri(),
			PlayOrigin: playOrigin,
		},
	})

	stream, err := s.player.NewStream(trackId, s.app.cfg.Bitrate, trackPosition)
	if err != nil {
		return fmt.Errorf("failed creating stream: %w", err)
	}

	log.Infof("loaded track \"%s\" (position: %dms, duration: %dms)", *stream.Track.Name, trackPosition, *stream.Track.Duration)

	s.updateState(func(s *State) {
		s.playerState.Duration = int64(*stream.Track.Duration)
		s.playerState.IsPlaying = true
		s.playerState.IsBuffering = false
	})

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeMetadata,
		Data: ApiEventDataMetadata(*NewApiResponseStatusTrack(stream.Track, s.prodInfo, int(trackPosition))),
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

	s.updateState(func(s *State) {
		s.playerState.Timestamp = time.Now().UnixMilli()
		s.playerState.PositionAsOfTimestamp = streamPos
		s.playerState.IsPaused = false
	})
	return nil
}

func (s *Session) pause() error {
	if s.stream == nil {
		return fmt.Errorf("no stream")
	}

	s.stream.Pause()

	streamPos := s.stream.PositionMs()
	log.Debugf("pause track at %dms", streamPos)

	s.updateState(func(s *State) {
		s.playerState.Timestamp = time.Now().UnixMilli()
		s.playerState.PositionAsOfTimestamp = streamPos
		s.playerState.IsPaused = true
	})
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

	var uri, playOrigin string
	s.updateState(func(s *State) {
		s.playerState.Timestamp = time.Now().UnixMilli()
		s.playerState.PositionAsOfTimestamp = position

		uri = s.playerState.Track.Uri
		playOrigin = s.playerState.PlayOrigin.FeatureIdentifier
	})

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeSeek,
		Data: ApiEventDataSeek{
			Uri:        uri,
			Position:   int(position),
			Duration:   int(*s.stream.Track.Duration),
			PlayOrigin: playOrigin,
		},
	})

	return nil
}

func (s *Session) skipPrev() error {
	if s.stream.PositionMs() > 3000 {
		return s.seek(0)
	}

	var paused bool
	s.withState(func(s *State) {
		paused = s.playerState.IsPaused

		if s.tracks != nil {
			log.Debug("skip previous track")
			s.tracks.GoPrev()

			s.playerState.Track = s.tracks.CurrentTrack()
			s.playerState.PrevTracks = s.tracks.PrevTracks()
			s.playerState.NextTracks = s.tracks.NextTracks()
			s.playerState.Index = s.tracks.Index()
		}

		s.playerState.Timestamp = time.Now().UnixMilli()
		s.playerState.PositionAsOfTimestamp = 0
	})

	// load current track into stream
	if err := s.loadCurrentTrack(); err != nil {
		return fmt.Errorf("failed loading current track: %w", err)
	}

	// start playing if not paused
	if !paused {
		if err := s.play(); err != nil {
			return fmt.Errorf("failed playing: %w", err)
		}
	}

	return nil
}

func (s *Session) skipNext() error {
	var paused bool
	s.withState(func(s *State) {
		paused = s.playerState.IsPaused

		if s.tracks != nil {
			log.Debug("skip next track")
			s.tracks.GoNext()

			s.playerState.Track = s.tracks.CurrentTrack()
			s.playerState.PrevTracks = s.tracks.PrevTracks()
			s.playerState.NextTracks = s.tracks.NextTracks()
			s.playerState.Index = s.tracks.Index()
		}

		s.playerState.Timestamp = time.Now().UnixMilli()
		s.playerState.PositionAsOfTimestamp = 0
	})

	// load current track into stream
	if err := s.loadCurrentTrack(); err != nil {
		return fmt.Errorf("failed loading current track: %w", err)
	}

	// start playing if not paused
	if !paused {
		if err := s.play(); err != nil {
			return fmt.Errorf("failed playing: %w", err)
		}
	}

	return nil
}

func (s *Session) updateVolume(newVal uint32) {
	if newVal > player.MaxStateVolume {
		newVal = player.MaxStateVolume
	} else if newVal < 0 {
		newVal = 0
	}

	log.Debugf("update volume to %d/%d", newVal, player.MaxStateVolume)
	s.player.SetVolume(newVal)
	s.withState(func(s *State) {
		s.deviceInfo.Volume = newVal
	})

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
