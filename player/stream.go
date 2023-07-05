package player

import metadatapb "go-librespot/proto/spotify/metadata"

type Stream struct {
	p   *Player
	idx int

	Track *metadatapb.Track
	File  *metadatapb.AudioFile
}

func (s *Stream) Play() <-chan any {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdPlay, data: s.idx, resp: resp}
	return resp
}

func (s *Stream) Stop() <-chan any {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdStop, data: s.idx, resp: resp}
	return resp
}
