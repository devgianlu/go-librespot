package go_librespot

import "testing"

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
		{"spotify:user:someone:collection", "user"},
		{"", ""},
		{"not-a-uri", ""},
		{"spotify:album", ""},
		{"http://open.spotify.com/album/xxx", ""},
	}

	for _, tt := range tests {
		if got := ContextUriType(tt.uri); got != tt.want {
			t.Errorf("ContextUriType(%q) = %q, want %q", tt.uri, got, tt.want)
		}
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
	}

	for _, uri := range uris {
		if got := InferSpotifyIdTypeFromContextUri(uri); got != SpotifyIdTypeTrack {
			t.Errorf("InferSpotifyIdTypeFromContextUri(%q) = %q, want %q", uri, got, SpotifyIdTypeTrack)
		}
	}
}

func TestInferSpotifyIdTypeEpisodeContexts(t *testing.T) {
	uris := []string{
		"spotify:show:5CnDmMUG0S5bSSw612fs8C",
		"spotify:episode:0Jv8TUEkzMplSPfX3ynBXu",
		"spotify:collection:your-episodes",
	}

	for _, uri := range uris {
		if got := InferSpotifyIdTypeFromContextUri(uri); got != SpotifyIdTypeEpisode {
			t.Errorf("InferSpotifyIdTypeFromContextUri(%q) = %q, want %q", uri, got, SpotifyIdTypeEpisode)
		}
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
		if got := InferSpotifyIdTypeFromContextUri(uri); got != SpotifyIdTypeUnknown {
			t.Errorf("InferSpotifyIdTypeFromContextUri(%q) = %q, want unknown", uri, got)
		}
	}
}
