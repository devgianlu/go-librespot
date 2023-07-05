package main

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	librespot "go-librespot"
	"go-librespot/player"
	connectpb "go-librespot/proto/spotify/connectstate/model"
)

func (s *Session) handlePlayerEvent(ev *player.Event) {
	switch ev.Type {
	case player.EventTypeBuffering:
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = false
			s.playerState.IsBuffering = true
		})
	case player.EventTypePlaying:
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = false
			s.playerState.IsBuffering = false
		})
	case player.EventTypePaused:
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = true
			s.playerState.IsBuffering = false
		})
	case player.EventTypeStopped:
		// do nothing
	default:
		panic("unhandled player event")
	}
}

func (s *Session) loadCurrentTrack() error {
	var trackId librespot.TrackId
	s.updateState(func(s *State) {
		trackId = librespot.TrackIdFromUri(s.playerState.Track.Uri)

		s.playerState.IsPlaying = true
		s.playerState.IsBuffering = true
	})

	stream, err := s.player.NewStream(trackId)
	if err != nil {
		return fmt.Errorf("failed creating stream: %w", err)
	}

	// TODO: seek to position

	s.updateState(func(s *State) {
		s.playerState.Duration = int64(*stream.Track.Duration)
		s.playerState.IsPlaying = true
		s.playerState.IsBuffering = false
	})

	s.stream = stream
	return nil
}

func (s *Session) play() error {
	if s.stream == nil {
		return fmt.Errorf("no stream")
	}

	s.stream.Play()
	return nil
}

func (s *Session) updateVolume(newVal uint32) {
	if newVal > player.MaxVolume {
		newVal = player.MaxVolume
	} else if newVal < 0 {
		newVal = 0
	}

	s.player.SetVolume(newVal)
	s.withState(func(s *State) {
		s.deviceInfo.Volume = newVal
	})

	if err := s.putConnectState(connectpb.PutStateReason_VOLUME_CHANGED); err != nil {
		log.WithError(err).Error("failed put state after volume change")
	}
}
