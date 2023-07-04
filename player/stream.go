package player

import metadatapb "go-librespot/proto/spotify/metadata"

type Stream struct {
	p   *Player
	idx int

	Track *metadatapb.Track
	File  *metadatapb.AudioFile
}

func (s *Stream) Play() {
	s.p.cmd <- playerCmd{typ: playerCmdPlay, data: s.idx}
}
