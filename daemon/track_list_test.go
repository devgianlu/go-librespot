//go:build test_unit

package daemon

import (
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/require"
)

var listUris = []string{
	"spotify:track:4uLU6hMCjMI75M1A2tKUQC",
	"spotify:track:3CRDbSIZ4r5MsZ0YwxuEkn",
	"spotify:track:11hcBLPtbMp4aQI6zGQLub",
	"spotify:track:0VjIjW4GlUZAMYd2vXMi3b",
	"spotify:track:7qiZfU4dY1lWllzX7mPBI3",
}

func skipToUri(uri string) skipToFunc {
	return func(track *connectpb.ContextTrack) bool { return track.Uri == uri }
}

// A list of uris plays as given, without resolving anything.
func TestTrackListPlaysInOrder(t *testing.T) {
	_, snap, err := resolveContext(t.Context(), &librespot.NullLogger{}, nil, trackListContext(listUris), nil, false, nil)
	require.NoError(t, err)

	require.Equal(t, listUris[0], snap.Current.GetUri())
	require.Equal(t, listUris[1:], trackUris(snap.Next))
}

func TestTrackListStartsAtTheTrackAskedFor(t *testing.T) {
	_, snap, err := resolveContext(t.Context(), &librespot.NullLogger{}, nil, trackListContext(listUris), skipToUri(listUris[2]), false, nil)
	require.NoError(t, err)

	require.Equal(t, listUris[2], snap.Current.GetUri())
	require.Equal(t, listUris[:2], trackUris(snap.Prev))
	require.Equal(t, listUris[3:], trackUris(snap.Next))
}

// Shuffled, a list starts at the track asked for and plays all the others.
func TestTrackListShuffles(t *testing.T) {
	_, snap, err := resolveContext(t.Context(), &librespot.NullLogger{}, nil, trackListContext(listUris), skipToUri(listUris[2]), true, nil)
	require.NoError(t, err)

	require.Equal(t, listUris[2], snap.Current.GetUri())
	require.Empty(t, snap.Prev)
	require.ElementsMatch(t, []string{listUris[0], listUris[1], listUris[3], listUris[4]}, trackUris(snap.Next))
}

func TestTrackListContextHasNoUri(t *testing.T) {
	require.Nil(t, trackListContext(nil))

	spotCtx := trackListContext(listUris[:1])
	require.Empty(t, spotCtx.Uri)
	require.Equal(t, librespot.SpotifyIdTypeTrack, librespot.InferSpotifyIdTypeFromContext(spotCtx))
}
