package player

import (
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
)

func GetFormatBitrate(format metadatapb.AudioFile_Format) int {
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

type AudioFormat int

const (
	AudioFormatUnknown AudioFormat = iota
	AudioFormatFLAC
	AudioFormatOGGVorbis
	AudioFormatMP3
	AudioFormatAAC
)

// GetAudioFileFormatAudioFormat maps metadatapb.AudioFile_Format to AudioFormat
func GetAudioFileFormatAudioFormat(format metadatapb.AudioFile_Format) AudioFormat {
	switch format {
	case metadatapb.AudioFile_OGG_VORBIS_96,
		metadatapb.AudioFile_OGG_VORBIS_160,
		metadatapb.AudioFile_OGG_VORBIS_320:
		return AudioFormatOGGVorbis
	case metadatapb.AudioFile_FLAC_FLAC,
		metadatapb.AudioFile_FLAC_FLAC_24BIT:
		return AudioFormatFLAC
	case metadatapb.AudioFile_MP3_96,
		metadatapb.AudioFile_MP3_160,
		metadatapb.AudioFile_MP3_256,
		metadatapb.AudioFile_MP3_320,
		metadatapb.AudioFile_MP3_160_ENC:
		return AudioFormatMP3
	case metadatapb.AudioFile_AAC_24,
		metadatapb.AudioFile_AAC_48,
		metadatapb.AudioFile_XHE_AAC_12,
		metadatapb.AudioFile_XHE_AAC_16,
		metadatapb.AudioFile_XHE_AAC_24:
		return AudioFormatAAC
	default:
		return AudioFormatUnknown
	}
}

func selectBestMediaFormat(files []*metadatapb.AudioFile, preferredBitrate int, flac bool) *metadatapb.AudioFile {
	absDist := func(a, b int) int {
		if a > b {
			return a - b
		} else {
			return b - a
		}
	}

	// Pick the best format by selecting which has the smallest distance from the preferred bitrate,
	// unless FLAC is requested, in which case pick FLAC if available.
	var best *metadatapb.AudioFile
	var bestDist int
	for _, ff := range files {
		if flac && (*ff.Format == metadatapb.AudioFile_FLAC_FLAC_24BIT || *ff.Format == metadatapb.AudioFile_FLAC_FLAC) {
			return ff
		}

		bitrate := GetFormatBitrate(*ff.Format)
		dist := absDist(bitrate, preferredBitrate)
		if best == nil || dist < bestDist {
			best = ff
			bestDist = dist
		}
	}

	return best
}
