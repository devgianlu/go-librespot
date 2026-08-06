//go:build test_unit

package player_test

import (
	"testing"

	"github.com/devgianlu/go-librespot/player"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/stretchr/testify/require"
)

func TestGetFormatCodec(t *testing.T) {
	tests := []struct {
		format metadatapb.AudioFile_Format
		want   string
	}{
		{metadatapb.AudioFile_OGG_VORBIS_96, "vorbis"},
		{metadatapb.AudioFile_OGG_VORBIS_320, "vorbis"},
		{metadatapb.AudioFile_FLAC_FLAC, "flac"},
		{metadatapb.AudioFile_FLAC_FLAC_24BIT, "flac"},
		{metadatapb.AudioFile_MP3_96, "mp3"},
		{metadatapb.AudioFile_MP3_320, "mp3"},
		{metadatapb.AudioFile_MP3_160_ENC, "mp3"},
		{metadatapb.AudioFile_AAC_24, "aac"},
		{metadatapb.AudioFile_XHE_AAC_12, "aac"},
	}

	for _, tt := range tests {
		t.Run(tt.format.String(), func(t *testing.T) {
			require.Equal(t, tt.want, player.GetFormatCodec(tt.format))
		})
	}
}

// Formats outside the enum still turn up on the wire: the podcast used for
// testing offered two (10 and 12) that this build has no name for.
func TestGetFormatCodecUnknown(t *testing.T) {
	require.Equal(t, "unknown", player.GetFormatCodec(metadatapb.AudioFile_Format(10)))
}

// The API reports bitrate as null when this returns 0, which is what should
// happen for lossless and for formats whose bitrate is not known.
func TestGetFormatBitrateReported(t *testing.T) {
	tests := []struct {
		format metadatapb.AudioFile_Format
		want   int
	}{
		{metadatapb.AudioFile_OGG_VORBIS_96, 96},
		{metadatapb.AudioFile_OGG_VORBIS_160, 160},
		{metadatapb.AudioFile_OGG_VORBIS_320, 320},
		{metadatapb.AudioFile_MP3_96, 96},
		{metadatapb.AudioFile_MP3_320, 320},
		{metadatapb.AudioFile_FLAC_FLAC, 0},
		{metadatapb.AudioFile_AAC_24, 0},
	}

	for _, tt := range tests {
		t.Run(tt.format.String(), func(t *testing.T) {
			require.Equal(t, tt.want, player.GetFormatBitrate(tt.format))
		})
	}
}
