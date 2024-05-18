package player

import (
	librespot "go-librespot"
	metadatapb "go-librespot/proto/spotify/metadata"
)

type Stream struct {
	Source librespot.AudioSource
	Media  *librespot.Media
	File   *metadatapb.AudioFile
}
