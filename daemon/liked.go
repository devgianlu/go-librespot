package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/devgianlu/go-librespot/spclient"
)

// likedTracksTTL bounds how long the Liked Songs set is trusted before it is
// read again, which is how changes made on other devices get picked up.
const likedTracksTTL = time.Minute

// collectionPageSize is how many items are asked for per collection page.
const collectionPageSize = 500

// collectionMaxPages caps the pages read for one set, in case the service
// keeps handing out page tokens.
const collectionMaxPages = 100

// likedTracks caches the track URIs in the user's Liked Songs. The set is
// read whole, so answering whether a few tracks are liked costs nothing while
// it is fresh.
type likedTracks struct {
	mu       sync.Mutex
	username string
	uris     map[string]bool
	fetched  time.Time

	// now is the clock the TTL is checked against; replaced by tests.
	now func() time.Time
}

func newLikedTracks() *likedTracks {
	return &likedTracks{now: time.Now}
}

// likedFetcher reads the complete Liked Songs set of a user.
type likedFetcher func(ctx context.Context, username string) (map[string]bool, error)

// contains reports for each uri whether it is liked, reading the set first
// when it is stale or belongs to another user. Concurrent callers wait for
// one read rather than each starting their own.
func (l *likedTracks) contains(ctx context.Context, username string, uris []string, fetch likedFetcher) ([]ApiLikedState, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.uris == nil || l.username != username || l.now().Sub(l.fetched) > likedTracksTTL {
		set, err := fetch(ctx, username)
		if err != nil {
			return nil, err
		}
		l.username, l.uris, l.fetched = username, set, l.now()
	}

	states := make([]ApiLikedState, len(uris))
	for i, uri := range uris {
		states[i] = ApiLikedState{Uri: uri, Liked: l.uris[uri]}
	}
	return states, nil
}

// apply records a write made through this daemon, so it is visible before
// the next read of the set.
func (l *likedTracks) apply(username string, uris []string, liked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.uris == nil || l.username != username {
		return
	}
	for _, uri := range uris {
		if liked {
			l.uris[uri] = true
		} else {
			delete(l.uris, uri)
		}
	}
}

// likedSongsContextUris are the context URIs Liked Songs is listed under.
func likedSongsContextUris(username string) []string {
	return []string{"spotify:user:" + username + ":collection", "spotify:collection:tracks"}
}

// fetchLikedTracks pages through the user's Liked Songs collection set, which
// also holds saved albums; only tracks are kept.
func fetchLikedTracks(spc *spclient.Spclient) likedFetcher {
	return func(ctx context.Context, username string) (map[string]bool, error) {
		set := map[string]bool{}
		token := ""
		for page := 0; page < collectionMaxPages; page++ {
			resp, err := spc.CollectionPage(ctx, username, spclient.CollectionSetLikedSongs, token, collectionPageSize)
			if err != nil {
				return nil, err
			}

			for _, item := range resp.GetItems() {
				if !item.GetIsRemoved() && strings.HasPrefix(item.GetUri(), "spotify:track:") {
					set[item.GetUri()] = true
				}
			}

			token = resp.GetNextPageToken()
			if token == "" {
				return set, nil
			}
		}

		return nil, fmt.Errorf("liked songs still paging after %d pages", collectionMaxPages)
	}
}
