//go:build test_unit

package daemon

import (
	"strings"
	"testing"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

func trackWith(kinds ...string) *connectpb.ProvidedTrack {
	md := map[string]string{}
	for _, k := range kinds {
		md["narration."+k+".ssml"] = `<speak xml:lang="en-US">hello</speak>`
	}

	return &connectpb.ProvidedTrack{Uri: "spotify:track:2FY7b99s15jUprqC0M5NCT", Metadata: md}
}

func TestNarrationKinds(t *testing.T) {
	tests := []struct {
		name  string
		track *connectpb.ProvidedTrack
		want  string
	}{
		{"nil track", nil, ""},
		{"no metadata", &connectpb.ProvidedTrack{}, ""},
		{"ordinary track", trackWith(), ""},
		{"all three", trackWith("intro", "jump", "outro"), "intro, jump, outro"},
		{"intro only", trackWith("intro"), "intro"},
		{"outro only", trackWith("outro"), "outro"},
		{"jump and outro", trackWith("jump", "outro"), "jump, outro"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(narrationKinds(tt.track.GetMetadata()), ", "); got != tt.want {
				t.Errorf("narrationKinds() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Metadata without a script does not count: a narration is only real if there
// is something to say.
func TestNarrationKindsIgnoresScriptlessMetadata(t *testing.T) {
	track := &connectpb.ProvidedTrack{Metadata: map[string]string{
		"narration.intro.voice":    "VOICE1",
		"narration.intro.loudness": "-16.0",
	}}

	if got := narrationKinds(track.GetMetadata()); len(got) != 0 {
		t.Errorf("narrationKinds() = %v, want none without a script", got)
	}
}

func TestNarrationPlan(t *testing.T) {
	tests := []struct {
		name        string
		track       *connectpb.ProvidedTrack
		introPrefix string
		want        string
	}{
		{"intro and outro", trackWith("intro", "jump", "outro"), narrationIntroPrefix, "intro + outro"},
		// Arriving by a jump swaps which of the two alternatives is spoken.
		{"jump and outro", trackWith("intro", "jump", "outro"), narrationJumpPrefix, "jump + outro"},
		{"intro only", trackWith("intro"), narrationIntroPrefix, "intro"},
		{"outro only", trackWith("outro"), narrationIntroPrefix, "outro"},
		// A track with no jump line, reached by jumping, still gets its outro.
		{"jumped but no jump line", trackWith("intro", "outro"), narrationJumpPrefix, "outro"},
		{"nothing", trackWith(), narrationIntroPrefix, "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := narrationPlan(tt.track.GetMetadata(), tt.introPrefix); got != tt.want {
				t.Errorf("narrationPlan() = %q, want %q", got, tt.want)
			}
		})
	}
}
