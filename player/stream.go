package player

import metadatapb "go-librespot/proto/spotify/metadata"

type playerCmdSeekData struct {
	idx int
	pos int64
}

type Stream struct {
	p   *Player
	idx int

	Track *metadatapb.Track
	File  *metadatapb.AudioFile
}

func (s *Stream) Play() {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdPlay, data: s.idx, resp: resp}
	<-resp
}

func (s *Stream) Stop() {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdStop, data: s.idx, resp: resp}
	<-resp
}

func (s *Stream) SeekMs(pos int64) error {
	resp := make(chan any, 1)
	s.p.cmd <- playerCmd{typ: playerCmdSeek, data: playerCmdSeekData{s.idx, pos}, resp: resp}
	if err := <-resp; err != nil {
		return err.(error)
	}

	return nil
}
