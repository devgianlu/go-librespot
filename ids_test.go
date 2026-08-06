//go:build test_unit

package go_librespot_test

import (
	"testing"

	librespot "github.com/devgianlu/go-librespot"

	"github.com/stretchr/testify/require"
)

func TestContextUriType(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"spotify:album:5r36AJ6VOJtp00oxSkBZ5h", "album"},
		{"spotify:playlist:37i9dQZF1E36KLdUfLiuUo", "playlist"},
		{"spotify:collection:tracks", "collection"},
		// User scoped URIs carry the username where the type usually sits.
		{"spotify:user:someone:playlist:37i9dQZF1E36KLdUfLiuUo", "playlist"},
		{"spotify:user:someone:collection", "collection"},
		{"spotify:user:someone:collection:your-episodes", "collection"},
		{"", ""},
		{"not-a-uri", ""},
		{"spotify:album", ""},
		{"http://open.spotify.com/album/xxx", ""},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			require.Equal(t, tt.want, librespot.ContextUriType(tt.uri))
		})
	}
}

// The contexts that already worked must keep working: getting any of these
// wrong silently breaks normal playback.
func TestInferSpotifyIdTypeTrackContexts(t *testing.T) {
	uris := []string{
		"spotify:album:5r36AJ6VOJtp00oxSkBZ5h",
		"spotify:artist:53XhwfbYqKCa1cC15pYq2q",
		"spotify:playlist:37i9dQZF1E36KLdUfLiuUo",
		"spotify:track:2FY7b99s15jUprqC0M5NCT",
		"spotify:station:playlist:37i9dQZF1E36KLdUfLiuUo",
		"spotify:dailymix:xxx",
		"spotify:collection:tracks",
		"spotify:user:someone:playlist:37i9dQZF1E36KLdUfLiuUo",
		"spotify:user:11145089019:collection",
	}

	for _, uri := range uris {
		t.Run(uri, func(t *testing.T) {
			require.Equal(t, librespot.SpotifyIdTypeTrack, librespot.InferSpotifyIdTypeFromContextUri(uri))
		})
	}
}

func TestInferSpotifyIdTypeEpisodeContexts(t *testing.T) {
	uris := []string{
		"spotify:show:5CnDmMUG0S5bSSw612fs8C",
		"spotify:episode:0Jv8TUEkzMplSPfX3ynBXu",
		"spotify:collection:your-episodes",
		"spotify:user:someone:collection:your-episodes",
	}

	for _, uri := range uris {
		t.Run(uri, func(t *testing.T) {
			require.Equal(t, librespot.SpotifyIdTypeEpisode, librespot.InferSpotifyIdTypeFromContextUri(uri))
		})
	}
}

// These used to be reported as tracks, which meant their ids were base62
// decoded as track gids and failed confusingly later on.
func TestInferSpotifyIdTypeUnsupportedContexts(t *testing.T) {
	uris := []string{
		"spotify:socialsession:5xwj7pphGg7mJSfWz2vXY8",
		"spotify:prerelease:6nQjPI2xUOZjJ7bJPMFxtF",
		"spotify:ad:xxx",
		"spotify:image:xxx",
		"spotify:user:someone",
		"spotify:somethingnew:xxx",
		"",
		"garbage",
	}

	for _, uri := range uris {
		t.Run(uri, func(t *testing.T) {
			require.Equal(t, librespot.SpotifyIdTypeUnknown, librespot.InferSpotifyIdTypeFromContextUri(uri))
		})
	}
}
