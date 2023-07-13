package player

import (
	metadatapb "go-librespot/proto/spotify/metadata"
)

func formatBitrate(format metadatapb.AudioFile_Format) int {
	switch format {
	case metadatapb.AudioFile_OGG_VORBIS_96:
		return 96
	case metadatapb.AudioFile_OGG_VORBIS_160:
		return 160
	case metadatapb.AudioFile_OGG_VORBIS_320:
		return 320
	default:
		return 0
	}
}
