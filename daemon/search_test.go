//go:build test_unit

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/devgianlu/go-librespot/pathfinder"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/stretchr/testify/require"
)

// The search endpoints are opt-in: disabled, the request is refused before
// anything reaches the session.
func TestSearchDisabledIsNotFound(t *testing.T) {
	p := &AppPlayer{app: &App{cfg: &Config{}}}

	for _, typ := range []ApiRequestType{ApiRequestTypeSearchTracks, ApiRequestTypeSearchSuggestions} {
		_, err := p.handleApiRequest(ApiRequest{Type: typ, Data: ApiRequestDataSearch{Query: "x", Limit: 10}})
		require.ErrorIs(t, err, ErrNotFound)
	}
}

// recordedQuery is a pathfinder query a test answered.
type recordedQuery struct {
	name, hash string
	variables  map[string]any
}

// fixtureQuerier answers every query with the recorded response of its
// operation, from testdata/pathfinder.
func fixtureQuerier(t *testing.T, queries *[]recordedQuery) pathfinderQueryFunc {
	return func(_ context.Context, name, hash string, variables any) (json.RawMessage, error) {
		*queries = append(*queries, recordedQuery{name, hash, variables.(map[string]any)})
		data, err := os.ReadFile(filepath.Join("testdata", "pathfinder", name+".json"))
		require.NoError(t, err)
		return data, nil
	}
}

func staticQuerier(data string, err error) pathfinderQueryFunc {
	return func(context.Context, string, string, any) (json.RawMessage, error) {
		return json.RawMessage(data), err
	}
}

var defaultImageUrl = newSearchImageUrlFunc("default")

func strPtr(s string) *string { return &s }

func TestSearchMapsEachType(t *testing.T) {
	for name, tc := range map[string]struct {
		typ   ApiRequestType
		op    searchOperation
		total int
		count int
		first any
	}{
		"tracks": {
			typ: ApiRequestTypeSearchTracks, op: searchTracksOp, total: 35, count: 3,
			first: ApiSearchTrack{
				Uri:           "spotify:track:1pKYYY0dkg23sQQXi0Q5zN",
				Name:          "Around the World",
				ArtistNames:   []string{"Daft Punk"},
				ArtistUris:    []string{"spotify:artist:4tZwfgrHOc3mvqYlEYSvVi"},
				AlbumName:     "Homework",
				AlbumUri:      "spotify:album:5uRdvUR7xCnHmUW8n64n9y",
				AlbumCoverUrl: strPtr("https://i.scdn.co/image/ab67616d00001e028ac778cc7d88779f74d33311"),
				Duration:      429533,
			},
		},
		"albums": {
			typ: ApiRequestTypeSearchAlbums, op: searchAlbumsOp, total: 190, count: 3,
			first: ApiLibraryAlbum{
				Uri:         "spotify:album:5uRdvUR7xCnHmUW8n64n9y",
				Name:        "Homework",
				ArtistNames: []string{"Daft Punk"},
				Year:        1997,
				ImageUrl:    strPtr("https://i.scdn.co/image/ab67616d00001e028ac778cc7d88779f74d33311"),
			},
		},
		"artists": {
			typ: ApiRequestTypeSearchArtists, op: searchArtistsOp, total: 114, count: 3,
			first: ApiLibraryArtist{
				Uri:      "spotify:artist:4tZwfgrHOc3mvqYlEYSvVi",
				Name:     "Daft Punk",
				ImageUrl: strPtr("https://i.scdn.co/image/ab67616100005174d3aa7cc0e419b6c459b08e8e"),
			},
		},
		"playlists": {
			typ: ApiRequestTypeSearchPlaylists, op: searchPlaylistsOp, total: 192, count: 3,
			first: ApiSearchPlaylist{
				Uri:           "spotify:playlist:2jTy5QwqWJ1ZUv2XeJPYbn",
				Name:          "Daft Punk Greatest Hits",
				Description:   "Best hits of this awesome band!",
				OwnerUsername: "user1",
				ImageUrl:      strPtr("https://image-cdn-fa.spotifycdn.com/image/ab67706c0000d72cbb071380835cf25eafa2f681"),
			},
		},
		"shows, skipping an error in place of one": {
			typ: ApiRequestTypeSearchShows, op: searchPodcastsOp, total: 145, count: 3,
			first: ApiSearchShow{
				Uri:       "spotify:show:0wxzoqBV4cE4F0i2hbBw4k",
				Name:      "Daft Punk, le secret des robots",
				Publisher: "Amazon Originals",
				ImageUrl:  strPtr("https://i.scdn.co/image/ab67656300005f1f8506600d4bed18d909237221"),
			},
		},
		"episodes, as tracks of their show": {
			typ: ApiRequestTypeSearchEpisodes, op: searchEpisodesOp, total: 1000, count: 2,
			first: ApiSearchTrack{
				Uri:           "spotify:episode:0r2a0n8MkS1tiwCN0vg2mV",
				Name:          "La mixtape de Daft Punk",
				ArtistNames:   []string{"Fip Tape"},
				ArtistUris:    []string{},
				AlbumName:     "Fip Tape",
				AlbumUri:      "spotify:show:643Xm3QDeHLR3PXMAYZidG",
				AlbumCoverUrl: strPtr("https://i.scdn.co/image/ab67656300005f1fa1128918c1e0ef8284ab6369"),
				Duration:      7410573,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var queries []recordedQuery
			res, err := runSearch(t.Context(), fixtureQuerier(t, &queries), defaultImageUrl, tc.typ, ApiRequestDataSearch{Query: "daft punk", Offset: 0, Limit: 20})
			require.NoError(t, err)

			require.Len(t, queries, 1)
			require.Equal(t, tc.op.name, queries[0].name)
			require.Equal(t, tc.op.hash, queries[0].hash)
			require.Equal(t, "daft punk", queries[0].variables["searchTerm"])
			require.Equal(t, 0, queries[0].variables["offset"])
			require.Equal(t, 20, queries[0].variables["limit"])

			// Every page has the same envelope, whatever its items.
			var page struct {
				Total, Offset, Limit int
				Items                []json.RawMessage
			}
			raw, err := json.Marshal(res)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &page))
			require.Equal(t, tc.total, page.Total)
			require.Equal(t, 20, page.Limit)
			require.Len(t, page.Items, tc.count)

			want, err := json.Marshal(tc.first)
			require.NoError(t, err)
			require.JSONEq(t, string(want), string(page.Items[0]))
		})
	}
}

// The episode search takes the reduced variables of the web player's
// getFullEpisodes; the others the full set of a category search.
func TestSearchVariables(t *testing.T) {
	require.Equal(t, map[string]any{
		"searchTerm": "q", "offset": 5, "limit": 10, "includeEpisodeContentRatingsV2": true,
	}, searchVariables(searchEpisodesOp, "q", 5, 10))

	require.Equal(t, map[string]any{
		"searchTerm": "q", "offset": 5, "limit": 10, "includeEpisodeContentRatingsV2": true,
		"numberOfTopResults": 20, "includeAudiobooks": true, "includePreReleases": false, "includeAuthors": true,
	}, searchVariables(searchTracksOp, "q", 5, 10))
}

// A hash with a typo is only found by Spotify refusing it.
func TestSearchOperationHashes(t *testing.T) {
	sha256 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, op := range []searchOperation{searchTracksOp, searchAlbumsOp, searchArtistsOp, searchPlaylistsOp, searchPodcastsOp, searchEpisodesOp, searchSuggestionsOp} {
		require.Regexp(t, sha256, op.hash, op.name)
	}
	for typ, search := range typedSearches {
		require.NotEmpty(t, search.op.name, typ)
	}
}

func TestSearchSuggestions(t *testing.T) {
	var queries []recordedQuery
	res, err := runSearch(t.Context(), fixtureQuerier(t, &queries), defaultImageUrl, ApiRequestTypeSearchSuggestions, ApiRequestDataSearch{Query: "daft p", Limit: 10})
	require.NoError(t, err)

	require.Len(t, queries, 1)
	require.Equal(t, searchSuggestionsOp.name, queries[0].name)
	require.Equal(t, "daft p", queries[0].variables["query"])
	require.Equal(t, 10, queries[0].variables["limit"])

	sugg := res.(*ApiSearchSuggestions)
	require.Equal(t, []string{"daft punk", "daft punk tron", "daft punk veridis quo"}, sugg.Queries)
	require.Len(t, sugg.Albums, 1)
	require.Equal(t, "Homework", sugg.Albums[0].Name)
	require.Equal(t, 1997, sugg.Albums[0].Year)
	require.Len(t, sugg.Artists, 1)
	require.Equal(t, "Daft Punk", sugg.Artists[0].Name)
	require.Len(t, sugg.Tracks, 1)

	// Empty lists are sent as such, never as null.
	raw, err := json.Marshal(sugg)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"playlists":[]`)
	require.Contains(t, string(raw), `"episodes":[]`)
}

func TestSearchSkipsWhatIsNotAResult(t *testing.T) {
	const data = `{"searchV2":{"albumsV2":{"totalCount":9,"pagingInfo":{"nextOffset":null},"items":[
		{"__typename":"AlbumResponseWrapper","data":{"__typename":"Album","uri":"spotify:album:1","name":"One","date":{"year":"soon"}}},
		{"__typename":"AlbumResponseWrapper","data":{"__typename":"NotFound"}},
		{"__typename":"AlbumResponseWrapper","data":{"__typename":"RestrictedContent","message":"no"}},
		{"__typename":"AlbumResponseWrapper","data":null},
		{"__typename":"AlbumResponseWrapper"},
		{"__typename":"AlbumResponseWrapper","data":{"__typename":"Track","uri":"spotify:track:1"}},
		{"__typename":"AlbumResponseWrapper","data":{"__typename":"Album","name":"no uri"}},
		{"__typename":"AlbumResponseWrapper","data":{"__typename":"Album","uri":"spotify:album:2","artists":{"items":[{"profile":{"name":"A"}}]}}}
	]}}}`

	res, err := runSearch(t.Context(), staticQuerier(data, nil), defaultImageUrl, ApiRequestTypeSearchAlbums, ApiRequestDataSearch{Query: "x", Limit: 20})
	require.NoError(t, err)

	page := res.(*ApiSearchAlbums)
	require.Equal(t, []ApiLibraryAlbum{
		// A field of an unexpected type costs that field only.
		{Uri: "spotify:album:1", Name: "One", ArtistNames: []string{}},
		{Uri: "spotify:album:2", ArtistNames: []string{"A"}},
	}, page.Items)
	// The last page: its total is what was paged through.
	require.Equal(t, 8, page.Total)
}

// An episode whose show did not resolve names no show, rather than an empty one.
func TestSearchEpisodeWithoutShow(t *testing.T) {
	const data = `{"searchV2":{"episodes":{"totalCount":1,"pagingInfo":{"nextOffset":null},"items":[
		{"__typename":"EpisodeResponseWrapper","data":{"__typename":"Episode","uri":"spotify:episode:1","name":"One",
			"podcastV2":{"__typename":"PodcastResponseWrapper","data":{"__typename":"NotFound"}}}}
	]}}}`

	res, err := runSearch(t.Context(), staticQuerier(data, nil), defaultImageUrl, ApiRequestTypeSearchEpisodes, ApiRequestDataSearch{Query: "x", Limit: 20})
	require.NoError(t, err)
	require.Equal(t, []ApiSearchTrack{{Uri: "spotify:episode:1", Name: "One", ArtistNames: []string{}, ArtistUris: []string{}}}, res.(*ApiSearchTracks).Items)
}

func TestSearchUnexpectedResponses(t *testing.T) {
	for name, data := range map[string]string{
		"not json":               `<html>`,
		"another query's result": `{"me":{"profile":{"name":"x"}}}`,
		"searchV2 not an object": `{"searchV2":"x"}`,
		"another search's node":  `{"searchV2":{"tracksV2":{"items":[]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runSearch(t.Context(), staticQuerier(data, nil), defaultImageUrl, ApiRequestTypeSearchAlbums, ApiRequestDataSearch{Query: "x", Limit: 20})
			require.Error(t, err)
			require.False(t, errors.Is(err, ErrTooManyRequests))
		})
	}

	t.Run("suggestions without their node", func(t *testing.T) {
		_, err := runSearch(t.Context(), staticQuerier(`{"searchV2":{}}`, nil), defaultImageUrl, ApiRequestTypeSearchSuggestions, ApiRequestDataSearch{Query: "x", Limit: 10})
		require.Error(t, err)
	})

	t.Run("no result is not an error", func(t *testing.T) {
		res, err := runSearch(t.Context(), staticQuerier(`{"searchV2":{"albumsV2":{"totalCount":0,"pagingInfo":{"nextOffset":null},"items":[]}}}`, nil),
			defaultImageUrl, ApiRequestTypeSearchAlbums, ApiRequestDataSearch{Query: "x", Limit: 20})
		require.NoError(t, err)
		require.Equal(t, &ApiSearchAlbums{Total: 0, Offset: 0, Limit: 20, Items: []ApiLibraryAlbum{}}, res)
	})
}

func TestSearchErrors(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		_, err := runSearch(t.Context(), staticQuerier("", &spclient.RateLimitedError{RetryAfter: 3 * time.Second}), defaultImageUrl, ApiRequestTypeSearchTracks, ApiRequestDataSearch{Query: "x", Limit: 20})
		require.ErrorIs(t, err, ErrTooManyRequests)
		var rl *spclient.RateLimitedError
		require.ErrorAs(t, err, &rl, "the advised wait reaches the Retry-After header")
	})

	t.Run("refused", func(t *testing.T) {
		refusal := &spclient.PathfinderError{Operation: "searchTracks", StatusCode: 412}
		_, err := runSearch(t.Context(), staticQuerier("", refusal), defaultImageUrl, ApiRequestTypeSearchTracks, ApiRequestDataSearch{Query: "x", Limit: 20})
		require.ErrorIs(t, err, refusal)
		require.False(t, errors.Is(err, ErrTooManyRequests))
	})
}

func TestSearchTotal(t *testing.T) {
	next := func(n int) pathfinder.PagingInfo { return pathfinder.PagingInfo{NextOffset: &n} }

	require.Equal(t, 35, searchTotal(35, next(20), 0, 20), "the estimate while it reaches into the next page")
	require.Equal(t, 61, searchTotal(35, next(60), 40, 20), "into the next page when the estimate falls short")
	require.Equal(t, 52, searchTotal(436, pathfinder.PagingInfo{}, 40, 12), "exact on the last page")
	require.Equal(t, 1000, searchTotal(5000, next(50), 0, 50), "within the results pathfinder pages through")
}

func TestSearchImageSize(t *testing.T) {
	width := func(n int) *int { return &n }
	image := pathfinder.ImageSet{Sources: []pathfinder.ImageSource{
		{Url: "300", Width: width(300)},
		{Url: "64", Width: width(64)},
		{Url: "640", Width: width(640)},
		{Url: "unknown"},
	}}

	for size, want := range map[string]string{"small": "64", "default": "300", "large": "640", "xlarge": "640", "": "300"} {
		require.Equal(t, want, *newSearchImageUrlFunc(size)(image), fmt.Sprintf("size %q", size))
	}

	unknown := pathfinder.ImageSet{Sources: []pathfinder.ImageSource{{Url: ""}, {Url: "only"}}}
	require.Equal(t, "only", *defaultImageUrl(unknown), "a source of unknown width when there is no other")
	require.Nil(t, defaultImageUrl(pathfinder.ImageSet{}))
}
