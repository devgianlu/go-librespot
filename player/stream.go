package player

import (
	librespot "go-librespot"
	metadatapb "go-librespot/proto/spotify/metadata"
)

type Stream struct {
	p *Player

	Media *librespot.Media
	File  *metadatapb.AudioFile
}

func (s *Stream) Play() {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdPlay, resp: resp}
	<-resp
}

func (s *Stream) Pause() {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdPause, resp: resp}
	<-resp
}

func (s *Stream) Stop() {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdStop, resp: resp}
	<-resp
}

func (s *Stream) SeekMs(pos int64) error {
	if pos < 0 {
		pos = 0
	}

	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdSeek, data: pos, resp: resp}
	if err := <-resp; err != nil {
		return err.(error)
	}

	return nil
}

func (s *Stream) PositionMs() int64 {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdPosition, resp: resp}
	pos := <-resp
	return pos.(int64)
}
