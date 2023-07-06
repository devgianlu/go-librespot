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
	case player.EventTypePaused:
		s.updateState(func(s *State) {
			s.playerState.IsPlaying = true
			s.playerState.IsPaused = true
			s.playerState.IsBuffering = false
		})
	case player.EventTypeNotPlaying:
		s.withState(func(s *State) {
			if s.tracks != nil {
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
			log.WithError(err).Error("failed loading current track")
			return
		}

		// start playing
		if err := s.play(); err != nil {
			log.WithError(err).Error("failed playing")
			return
		}
	case player.EventTypeStopped:
		// do nothing
	default:
		panic("unhandled player event")
	}
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

		if s.playerState.IsPaused {
			trackPosition = s.playerState.PositionAsOfTimestamp
		} else {
			trackPosition = time.Now().UnixMilli() - s.playerState.Timestamp + s.playerState.PositionAsOfTimestamp
		}

		s.playerState.IsPlaying = true
		s.playerState.IsBuffering = true
	})

	stream, err := s.player.NewStream(trackId)
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
