//go:build test_unit

package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLikedStatesAsksWithCanonicalUris(t *testing.T) {
	var asked []string
	contains := func(_ context.Context, uris []string) ([]bool, error) {
		asked = uris
		return []bool{true, false}, nil
	}

	// A 21-character id is the same track as its zero-padded 22-character form.
	short := "spotify:track:4uLU6hMCjMI75M1A2tKUQC"[:14] + "uLU6hMCjMI75M1A2tKUQC"
	states, err := likedStates(context.Background(), contains, []string{short, "spotify:track:4uLU6hMCjMI75M1A2tKUQC"})
	require.NoError(t, err)

	require.Equal(t, "spotify:track:0uLU6hMCjMI75M1A2tKUQC", asked[0], "asked in canonical form")
	require.Equal(t, []ApiLikedState{
		{Uri: short, Liked: true},
		{Uri: "spotify:track:4uLU6hMCjMI75M1A2tKUQC", Liked: false},
	}, states, "answered with the URIs as requested")
}

func TestLikedStatesPassesErrorsOn(t *testing.T) {
	contains := func(context.Context, []string) ([]bool, error) { return nil, errors.New("boom") }
	_, err := likedStates(context.Background(), contains, []string{"spotify:track:4uLU6hMCjMI75M1A2tKUQC"})
	require.Error(t, err)
}
