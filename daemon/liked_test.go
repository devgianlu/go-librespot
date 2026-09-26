//go:build test_unit

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeLikedFetcher struct {
	calls int
	sets  map[string]map[string]bool
	err   error
}

func (f *fakeLikedFetcher) fetch(_ context.Context, username string) (map[string]bool, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	set := map[string]bool{}
	for uri := range f.sets[username] {
		set[uri] = true
	}
	return set, nil
}

func likedFlags(states []ApiLikedState) []bool {
	out := make([]bool, len(states))
	for i, s := range states {
		out[i] = s.Liked
	}
	return out
}

func TestLikedTracksContains(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLikedTracks()
	l.now = func() time.Time { return now }
	f := &fakeLikedFetcher{sets: map[string]map[string]bool{
		"alice": {"spotify:track:a": true},
		"bob":   {"spotify:track:b": true},
	}}

	states, err := l.contains(context.Background(), "alice", []string{"spotify:track:a", "spotify:track:b"}, f.fetch)
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, likedFlags(states))
	require.Equal(t, "spotify:track:b", states[1].Uri)

	_, err = l.contains(context.Background(), "alice", []string{"spotify:track:a"}, f.fetch)
	require.NoError(t, err)
	require.Equal(t, 1, f.calls, "a fresh set is reused")

	now = now.Add(likedTracksTTL + time.Second)
	_, err = l.contains(context.Background(), "alice", []string{"spotify:track:a"}, f.fetch)
	require.NoError(t, err)
	require.Equal(t, 2, f.calls, "a stale set is read again")

	states, err = l.contains(context.Background(), "bob", []string{"spotify:track:a", "spotify:track:b"}, f.fetch)
	require.NoError(t, err)
	require.Equal(t, 3, f.calls, "another user's set is never reused")
	require.Equal(t, []bool{false, true}, likedFlags(states))
}

func TestLikedTracksApply(t *testing.T) {
	l := newLikedTracks()
	f := &fakeLikedFetcher{sets: map[string]map[string]bool{"alice": {"spotify:track:a": true}}}

	l.apply("alice", []string{"spotify:track:x"}, true) // nothing loaded yet: ignored
	_, err := l.contains(context.Background(), "alice", []string{"spotify:track:a"}, f.fetch)
	require.NoError(t, err)

	l.apply("alice", []string{"spotify:track:x"}, true)
	l.apply("alice", []string{"spotify:track:a"}, false)
	l.apply("bob", []string{"spotify:track:a"}, true) // other user: ignored

	states, err := l.contains(context.Background(), "alice", []string{"spotify:track:a", "spotify:track:x"}, f.fetch)
	require.NoError(t, err)
	require.Equal(t, []bool{false, true}, likedFlags(states))
	require.Equal(t, 1, f.calls)
}

func TestLikedTracksFetchError(t *testing.T) {
	l := newLikedTracks()
	f := &fakeLikedFetcher{err: errors.New("boom")}

	_, err := l.contains(context.Background(), "alice", []string{"spotify:track:a"}, f.fetch)
	require.Error(t, err)

	f.err, f.sets = nil, map[string]map[string]bool{"alice": {"spotify:track:a": true}}
	states, err := l.contains(context.Background(), "alice", []string{"spotify:track:a"}, f.fetch)
	require.NoError(t, err)
	require.True(t, states[0].Liked, "a failed read is not cached")
}
