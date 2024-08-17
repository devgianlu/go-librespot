package player

import (
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
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

func selectBestMediaFormat(files []*metadatapb.AudioFile, preferredBitrate int) *metadatapb.AudioFile {
	absDist := func(a, b int) int {
		if a > b {
			return a - b
		} else {
			return b - a
		}
	}

	// pick the best format by selecting which has the smallest distance from the preferred bitrate
	var best *metadatapb.AudioFile
	var bestDist int
	for _, ff := range files {
		bitrate := formatBitrate(*ff.Format)
		dist := absDist(bitrate, preferredBitrate)
		if best == nil || dist < bestDist {
			best = ff
			bestDist = dist
		}
	}

	return best
}
