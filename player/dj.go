package player

import (
	"encoding/hex"
	"strconv"
	"strings"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

// IsDJTrack reports whether the provided track is part of a Spotify DJ session.
func IsDJTrack(track *connectpb.ProvidedTrack) bool {
	return strings.HasPrefix(track.Metadata["source.components"], "YourDJ,")
}

// DJNarration holds the metadata for a single TTS narration clip.
type DJNarration struct {
	CommentaryId string // UUID string — is a Spotify GID (16 bytes)
	SSML         string
	Voice        string
	Loudness     string
	SampleRate   string
	TtsProvider  string
	DecisionId   string
	Image        string
}

// NarrationForTrack extracts the DJ narration for the given type ("intro", "jump", "outro").
// Returns nil if no narration is present for this track/type.
func NarrationForTrack(track *connectpb.ProvidedTrack, typ string) *DJNarration {
	id := track.Metadata["narration."+typ+".commentary_id"]
	if id == "" {
		return nil
	}
	return &DJNarration{
		CommentaryId: id,
		SSML:         track.Metadata["narration."+typ+".ssml"],
		Voice:        track.Metadata["narration."+typ+".voice"],
		Loudness:     track.Metadata["narration."+typ+".loudness"],
		SampleRate:   track.Metadata["narration."+typ+".sample_rate"],
		TtsProvider:  track.Metadata["narration."+typ+".tts_provider"],
		DecisionId:   track.Metadata["narration."+typ+".decision_id"],
		Image:        track.Metadata["narration."+typ+".image"],
	}
}

// AutomixCueMs returns the fade-out cue point position in milliseconds for the track.
// Returns 0 if not present.
func AutomixCueMs(track *connectpb.ProvidedTrack) int64 {
	ms, _ := strconv.ParseInt(track.Metadata["automix.fade_out_cuepoint.position"], 10, 64)
	return ms
}

// NarrationSpotifyId converts a commentary_id UUID string to a SpotifyId (GID).
func NarrationSpotifyId(commentaryId string) (librespot.SpotifyId, error) {
	hexStr := strings.ReplaceAll(commentaryId, "-", "")
	gid, err := hex.DecodeString(hexStr)
	if err != nil {
		return librespot.SpotifyId{}, err
	}
	return librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, gid), nil
}
