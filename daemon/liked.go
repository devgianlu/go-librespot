package daemon

import (
	"context"
	"fmt"

	librespot "github.com/devgianlu/go-librespot"
)

// likedSongsContextUris are the context URIs Liked Songs is listed under.
func likedSongsContextUris(username string) []string {
	return []string{"spotify:user:" + username + ":collection", "spotify:collection:tracks"}
}

// likedContains asks the collection service which of uris are in Liked Songs.
type likedContains func(ctx context.Context, uris []string) ([]bool, error)

// likedStates tells for each of uris whether it is in Liked Songs. The
// collection holds canonical URIs, so a 21-character id is padded to its
// canonical form before asking; the answer names the URIs as requested.
func likedStates(ctx context.Context, contains likedContains, uris []string) ([]ApiLikedState, error) {
	canonical := make([]string, len(uris))
	for i, uri := range uris {
		id, err := librespot.SpotifyIdFromUri(uri)
		if err != nil {
			return nil, fmt.Errorf("invalid track uri %s: %w", uri, err)
		}
		canonical[i] = id.Uri()
	}

	found, err := contains(ctx, canonical)
	if err != nil {
		return nil, err
	}

	states := make([]ApiLikedState, len(uris))
	for i, uri := range uris {
		states[i] = ApiLikedState{Uri: uri, Liked: found[i]}
	}
	return states, nil
}
