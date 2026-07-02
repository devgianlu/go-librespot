package player

import (
	"bytes"

	librespot "github.com/devgianlu/go-librespot"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
)

type Stream struct {
	PlaybackId []byte

	// RequestedId is the id this stream was created for, before any relinking
	// to an alternative track. Callers keep referring to the stream by the id
	// they requested, which may differ from the media's own id.
	RequestedId librespot.SpotifyId

	Source librespot.AudioSource
	Media  *librespot.Media
	File   *metadatapb.AudioFile
}

func (s *Stream) Is(id librespot.SpotifyId) bool {
	// A restricted track may have been relinked to an alternative with a
	// different gid (see getUnrestrictedTrack); without this check a
	// prefetched stream for such a track is never recognized and gets loaded
	// again from scratch, discarding the already-playing prefetched audio.
	if id.Type() == s.RequestedId.Type() && bytes.Equal(id.Id(), s.RequestedId.Id()) {
		return true
	}

	if id.Type() == librespot.SpotifyIdTypeTrack && s.Media.IsTrack() {
		return bytes.Equal(id.Id(), s.Media.Track().Gid)
	} else if id.Type() == librespot.SpotifyIdTypeEpisode && s.Media.IsEpisode() {
		return bytes.Equal(id.Id(), s.Media.Episode().Gid)
	} else {
		return false
	}
}
