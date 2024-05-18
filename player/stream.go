package player

import (
	"bytes"
	librespot "go-librespot"
	metadatapb "go-librespot/proto/spotify/metadata"
)

type Stream struct {
	Source librespot.AudioSource
	Media  *librespot.Media
	File   *metadatapb.AudioFile
}

func (s *Stream) Is(id librespot.SpotifyId) bool {
	if id.Type() == librespot.SpotifyIdTypeTrack && s.Media.IsTrack() {
		return bytes.Equal(id.Id(), s.Media.Track().Gid)
	} else if id.Type() == librespot.SpotifyIdTypeEpisode && s.Media.IsEpisode() {
		return bytes.Equal(id.Id(), s.Media.Episode().Gid)
	} else {
		return false
	}
}
