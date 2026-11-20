//go:build test_unit

package tracks

import (
	"context"
	"io"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/require"
)

// itemsResolver is a list of items without a context: one page, no uri.
type itemsResolver struct {
	uri  string
	uris []string
}

func (r itemsResolver) Type() librespot.SpotifyIdType { return librespot.SpotifyIdTypeTrack }
func (r itemsResolver) Uri() string                   { return r.uri }
func (r itemsResolver) Metadata() map[string]string   { return map[string]string{} }

func (r itemsResolver) Page(_ context.Context, idx int) ([]*connectpb.ContextTrack, error) {
	if idx > 0 {
		return nil, io.EOF
	}
	tracks := make([]*connectpb.ContextTrack, len(r.uris))
	for i, uri := range r.uris {
		tracks[i] = &connectpb.ContextTrack{Uri: uri, Metadata: map[string]string{}}
	}
	return tracks, nil
}

func resolveItems(uris []string) (ContextResolver, error) { return itemsResolver{uris: uris}, nil }

func uris(n ...int) []string {
	out := make([]string, len(n))
	for i, v := range n {
		out[i] = trackUri(v)
	}
	return out
}

// playingList plays a list of items, at the item playing.
func playingList(t *testing.T, items []string, playing string, shuffled bool) *List {
	t.Helper()
	tl := newTrackList(&librespot.NullLogger{}, itemsResolver{uris: items})
	require.NoError(t, tl.Seek(t.Context(), func(track *connectpb.ContextTrack) bool { return track.Uri == playing }))
	require.NoError(t, tl.ToggleShuffle(t.Context(), shuffled))
	return tl
}

func listed(tl *List) (prev, current []string, next []string) {
	for i, it := range tl.tracks.list {
		switch {
		case i < tl.tracks.pos:
			prev = append(prev, it.item.Uri)
		case i == tl.tracks.pos:
			current = append(current, it.item.Uri)
		default:
			next = append(next, it.item.Uri)
		}
	}
	return prev, current, next
}

func TestReplaceItemsInOrder(t *testing.T) {
	tl := playingList(t, uris(1, 2, 3, 4, 5), trackUri(2), false)

	// 4 was added to the compared playlist, 6 is new.
	require.NoError(t, tl.ReplaceItems(t.Context(), uris(1, 2, 3, 5, 6), resolveItems))

	prev, current, next := listed(tl)
	require.Equal(t, uris(1), prev)
	require.Equal(t, uris(2), current)
	require.Equal(t, uris(3, 5, 6), next)
}

// The playing item leaving the list keeps playing, and the list goes on
// with what came after it.
func TestReplaceItemsKeepsThePlayingItem(t *testing.T) {
	tl := playingList(t, uris(1, 2, 3, 4), trackUri(2), false)

	require.NoError(t, tl.ReplaceItems(t.Context(), uris(1, 3, 4), resolveItems))

	prev, current, next := listed(tl)
	require.Equal(t, uris(1), prev)
	require.Equal(t, uris(2), current)
	require.Equal(t, uris(3, 4), next)
	require.Equal(t, trackUri(2), tl.CurrentTrack().Uri)
}

func TestReplaceItemsKeepsTheShuffledOrder(t *testing.T) {
	tl := playingList(t, uris(1, 2, 3, 4, 5, 6, 7, 8), trackUri(3), true)
	_, _, before := listed(tl)

	// 5 leaves, 9 comes.
	require.NoError(t, tl.ReplaceItems(t.Context(), uris(1, 2, 3, 4, 6, 7, 8, 9), resolveItems))

	prev, current, next := listed(tl)
	require.Empty(t, prev)
	require.Equal(t, uris(3), current)
	require.ElementsMatch(t, uris(1, 2, 4, 6, 7, 8, 9), next)

	// What was still to come keeps its order, less 5 and with 9 mixed in.
	var kept []string
	for _, uri := range next {
		if uri != trackUri(9) {
			kept = append(kept, uri)
		}
	}
	var want []string
	for _, uri := range before {
		if uri != trackUri(5) {
			want = append(want, uri)
		}
	}
	require.Equal(t, want, kept)
}

func TestReplaceItemsUnshufflesIntoTheNewOrder(t *testing.T) {
	tl := playingList(t, uris(1, 2, 3, 4), trackUri(2), true)
	require.NoError(t, tl.ReplaceItems(t.Context(), uris(1, 2, 4, 5), resolveItems))

	require.NoError(t, tl.ToggleShuffle(t.Context(), false))

	prev, current, next := listed(tl)
	require.Equal(t, uris(1), prev)
	require.Equal(t, uris(2), current)
	require.Equal(t, uris(4, 5), next)
}

func TestReplaceItemsOnlyForAList(t *testing.T) {
	tl := newTrackList(&librespot.NullLogger{}, itemsResolver{uri: "spotify:playlist:x", uris: uris(1, 2)})
	require.NoError(t, tl.Seek(t.Context(), func(*connectpb.ContextTrack) bool { return true }))

	require.ErrorIs(t, tl.ReplaceItems(t.Context(), uris(1), resolveItems), ErrNotATrackList)
}
