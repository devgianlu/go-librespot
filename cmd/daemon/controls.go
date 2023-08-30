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
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = false
			s.playerState.IsBuffering = false
		})

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePlaying,
		})
	case player.EventTypePaused:
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = true
			s.playerState.IsBuffering = false
		})

		s.app.server.Emit(&ApiEvent{
			Type: ApiEventTypePaused,
		})
	case player.EventTypeNotPlaying:
		var hasNextTrack bool
		s.withState(func(s *State) {
			if s.tracks != nil {
				hasNextTrack = s.tracks.GoNext()
				s.playerState.IsPaused = !hasNextTrack

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
			log.WithError(err).Error("failed loading current track")
			return
		}

		// start playing if there is something next
		if hasNextTrack {
			if err := s.play(); err != nil {
				log.WithError(err).Error("failed playing")
				return
			}
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

	var trackPosition int64
	var trackId librespot.TrackId
	s.updateState(func(s *State) {
		trackId = librespot.TrackIdFromUri(s.playerState.Track.Uri)
		trackPosition = s.trackPosition()

		s.playerState.IsPlaying = true
		s.playerState.IsBuffering = true
	})

	stream, err := s.player.NewStream(trackId, s.app.cfg.Bitrate)
	if err != nil {
		return fmt.Errorf("failed creating stream: %w", err)
	}

	log.Debugf("seek track to %dms", trackPosition)
	if err := stream.SeekMs(trackPosition); err != nil {
		return fmt.Errorf("failed seeking track: %w", err)
	}

	s.updateState(func(s *State) {
		s.playerState.Duration = int64(*stream.Track.Duration)
		s.playerState.IsPlaying = true
		s.playerState.IsBuffering = false
	})

	s.app.server.Emit(&ApiEvent{
		Type: ApiEventTypeTrack,
		Data: ApiEventDataTrack(*NewApiResponseStatusTrack(stream.Track, s.prodInfo, int(trackPosition))),
	})

	s.stream = stream
	return nil
}

func (s *Session) play() error {
	if s.stream == nil {
		return fmt.Errorf("no stream")
	}

	log.Debug("resume track")
	s.stream.Play()

	s.updateState(func(s *State) {
		s.playerState.IsPaused = false
		// TODO: update player position
	})
	return nil
}

func (s *Session) pause() error {
	if s.stream == nil {
		return fmt.Errorf("no stream")
	}

	log.Debug("pause track")
	s.stream.Pause()

	s.updateState(func(s *State) {
		s.playerState.IsPaused = true
		// TODO: update player position
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

	s.updateState(func(s *State) {
		s.playerState.Timestamp = time.Now().UnixMilli()
		s.playerState.PositionAsOfTimestamp = position
	})

	return nil
}

func (s *Session) skipPrev() error {
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
