package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/devgianlu/go-librespot/pathfinder"
	"github.com/devgianlu/go-librespot/spclient"
)

// searchTimeout bounds a search made on behalf of an API caller.
const searchTimeout = 15 * time.Second

// searchMaxResults is how far pathfinder pages through the results of a
// search: it refuses an offset plus limit above it.
const searchMaxResults = 1000

// pathfinderQueryFunc runs a persisted pathfinder query, as
// spclient.Spclient.PathfinderQuery does.
type pathfinderQueryFunc func(ctx context.Context, operationName, sha256Hash string, variables any) (json.RawMessage, error)

// searchOperation is a persisted pathfinder query: its name and the sha256 of
// its query document, as Spotify's web player registers them. Spotify changes
// the hash whenever it rebuilds the query, and pathfinder then refuses the old
// one with a 412 until it is updated here.
type searchOperation struct {
	name string
	hash string
}

var (
	searchTracksOp      = searchOperation{"searchTracks", "b02683192a98dde7966b5e6655a79eeb62713eab703eda9902c932818dd52751"}
	searchAlbumsOp      = searchOperation{"searchAlbums", "202cb3305e31e5a0767ba7925f28bd728cf8f8b0217e6da43909056071cd70e9"}
	searchArtistsOp     = searchOperation{"searchArtists", "7bf95d754fdbe32c8b161fbbe54d1ae50974900df4dce4c8f1afcbcad153224d"}
	searchPlaylistsOp   = searchOperation{"searchPlaylists", "d520014e748f9ea44f7707d8df1819867ac1205e8b7f3e28f22fe5fc858921b1"}
	searchPodcastsOp    = searchOperation{"searchPodcasts", "0195d9f61b43606d490bca64c3456e3593528cea6cc05c7e822c7c42beed0f4e"}
	searchEpisodesOp    = searchOperation{"searchFullEpisodes", "397a4e93a21d140d088cfd26641133c5146a2f5f08e420654868153e720f5571"}
	searchSuggestionsOp = searchOperation{"searchSuggestions", "f244254b94c0e824d458ac216e0f8406a73f2f69a9ff52831a2f78fa774ff6ce"}
)

// searchVariables are the variables the web player sends with a search of one
// type. Its episode search takes fewer.
func searchVariables(op searchOperation, query string, offset, limit int) map[string]any {
	vars := map[string]any{
		"searchTerm":                     query,
		"offset":                         offset,
		"limit":                          limit,
		"includeEpisodeContentRatingsV2": true,
	}
	if op != searchEpisodesOp {
		vars["numberOfTopResults"] = 20
		vars["includeAudiobooks"] = true
		vars["includePreReleases"] = false
		vars["includeAuthors"] = true
	}
	return vars
}

func suggestionsVariables(query string, limit int) map[string]any {
	return map[string]any{
		"query":                          query,
		"limit":                          limit,
		"numberOfTopResults":             limit,
		"offset":                         0,
		"includeAuthors":                 true,
		"includeAlbumPreReleases":        false,
		"includeEpisodeContentRatingsV2": true,
	}
}

// searchImageUrlFunc picks the URL of an image, or nil.
type searchImageUrlFunc func(image pathfinder.ImageSet) *string

// searchImageWidths are the widths of Spotify's image sizes, xlarge being the
// widest source there is.
var searchImageWidths = map[string]int{"small": 64, "default": 300, "large": 640, "xlarge": math.MaxInt}

// newSearchImageUrlFunc returns how search results pick their image: the
// source closest in width to the configured image size, the default one when
// the size is unknown, as getBestImageIdForSize does for metadata images.
func newSearchImageUrlFunc(size string) searchImageUrlFunc {
	want, ok := searchImageWidths[strings.ToLower(size)]
	if !ok {
		want = searchImageWidths["default"]
	}

	return func(image pathfinder.ImageSet) *string {
		var best *pathfinder.ImageSource
		for i, src := range image.Sources {
			if src.Url == "" {
				continue
			} else if best == nil || closerWidth(src.Width, best.Width, want) {
				best = &image.Sources[i]
			}
		}

		if best == nil {
			return nil
		}
		return &best.Url
	}
}

// closerWidth reports whether width a is closer to want than width b, the
// wider one winning a tie; an unknown width loses to any known one.
func closerWidth(a, b *int, want int) bool {
	if a == nil || b == nil {
		return a != nil
	}

	da, db := absDiff(*a, want), absDiff(*b, want)
	return da < db || (da == db && *a > *b)
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// searchPageFunc builds the page of a search from its operation's result, or
// returns nil when the result lacks the operation's node: Spotify changed the
// response or failed to produce that part of it, as opposed to finding nothing.
type searchPageFunc func(res *pathfinder.SearchResultV2, imageUrl searchImageUrlFunc, offset, limit int) any

// typedSearches ties each typed search request to its operation and page.
var typedSearches = map[ApiRequestType]struct {
	op   searchOperation
	page searchPageFunc
}{
	ApiRequestTypeSearchTracks: {searchTracksOp, func(res *pathfinder.SearchResultV2, imageUrl searchImageUrlFunc, offset, limit int) any {
		if res.TracksV2 == nil {
			return nil
		}
		items := make([]pathfinder.EntityWrapper, len(res.TracksV2.Items))
		for i, hit := range res.TracksV2.Items {
			items[i] = hit.Item
		}
		total := searchTotal(res.TracksV2.TotalCount, res.TracksV2.PagingInfo, offset, len(items))
		return &ApiSearchTracks{Total: total, Offset: offset, Limit: limit, Items: searchItems(items, searchTrackItem(imageUrl))}
	}},
	ApiRequestTypeSearchAlbums: {searchAlbumsOp, func(res *pathfinder.SearchResultV2, imageUrl searchImageUrlFunc, offset, limit int) any {
		if p := res.AlbumsV2; p != nil {
			return &ApiSearchAlbums{Total: searchPageTotal(p, offset), Offset: offset, Limit: limit, Items: searchItems(p.Items, searchAlbumItem(imageUrl))}
		}
		return nil
	}},
	ApiRequestTypeSearchArtists: {searchArtistsOp, func(res *pathfinder.SearchResultV2, imageUrl searchImageUrlFunc, offset, limit int) any {
		if p := res.Artists; p != nil {
			return &ApiSearchArtists{Total: searchPageTotal(p, offset), Offset: offset, Limit: limit, Items: searchItems(p.Items, searchArtistItem(imageUrl))}
		}
		return nil
	}},
	ApiRequestTypeSearchPlaylists: {searchPlaylistsOp, func(res *pathfinder.SearchResultV2, imageUrl searchImageUrlFunc, offset, limit int) any {
		if p := res.Playlists; p != nil {
			return &ApiSearchPlaylists{Total: searchPageTotal(p, offset), Offset: offset, Limit: limit, Items: searchItems(p.Items, searchPlaylistItem(imageUrl))}
		}
		return nil
	}},
	ApiRequestTypeSearchShows: {searchPodcastsOp, func(res *pathfinder.SearchResultV2, imageUrl searchImageUrlFunc, offset, limit int) any {
		if p := res.Podcasts; p != nil {
			return &ApiSearchShows{Total: searchPageTotal(p, offset), Offset: offset, Limit: limit, Items: searchItems(p.Items, searchShowItem(imageUrl))}
		}
		return nil
	}},
	ApiRequestTypeSearchEpisodes: {searchEpisodesOp, func(res *pathfinder.SearchResultV2, imageUrl searchImageUrlFunc, offset, limit int) any {
		if p := res.Episodes; p != nil {
			return &ApiSearchTracks{Total: searchPageTotal(p, offset), Offset: offset, Limit: limit, Items: searchItems(p.Items, searchEpisodeItem(imageUrl))}
		}
		return nil
	}},
}

// runSearch answers a search request with one pathfinder query.
func runSearch(ctx context.Context, query pathfinderQueryFunc, imageUrl searchImageUrlFunc, typ ApiRequestType, data ApiRequestDataSearch) (any, error) {
	if typ == ApiRequestTypeSearchSuggestions {
		res, err := searchResult(ctx, query, searchSuggestionsOp, suggestionsVariables(data.Query, data.Limit))
		if err != nil {
			return nil, err
		} else if res.TopResultsV2 == nil {
			return nil, fmt.Errorf("pathfinder %s answered without its results", searchSuggestionsOp.name)
		}
		return searchSuggestions(res.TopResultsV2.ItemsV2, imageUrl), nil
	}

	search := typedSearches[typ]
	res, err := searchResult(ctx, query, search.op, searchVariables(search.op, data.Query, data.Offset, data.Limit))
	if err != nil {
		return nil, err
	}

	page := search.page(res, imageUrl, data.Offset, data.Limit)
	if page == nil {
		return nil, fmt.Errorf("pathfinder %s answered without its results", search.op.name)
	}
	return page, nil
}

// searchResult runs a search operation and decodes its searchV2.
func searchResult(ctx context.Context, query pathfinderQueryFunc, op searchOperation, variables map[string]any) (*pathfinder.SearchResultV2, error) {
	data, err := query(ctx, op.name, op.hash, variables)
	if err != nil {
		var rateLimit *spclient.RateLimitedError
		if errors.As(err, &rateLimit) {
			return nil, fmt.Errorf("failed searching: %w: %w", ErrTooManyRequests, err)
		}
		return nil, fmt.Errorf("failed searching: %w", err)
	}

	var doc pathfinder.SearchData
	err = json.Unmarshal(data, &doc)
	var typeErr *json.UnmarshalTypeError
	if !tolerable(err) || (errors.As(err, &typeErr) && typeErr.Field == "searchV2") {
		return nil, fmt.Errorf("failed decoding pathfinder %s response: %w", op.name, err)
	} else if doc.SearchV2 == nil {
		return nil, fmt.Errorf("pathfinder %s answered without searchV2", op.name)
	}
	return doc.SearchV2, nil
}

// tolerable reports whether a decoding error leaves the value usable: a field
// of an unexpected type, which encoding/json skips while decoding the rest. It
// costs that field rather than the result holding it.
func tolerable(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return err == nil || errors.As(err, &typeErr)
}

// searchTotal is how many results a page says there are. Spotify's count is
// an estimate, at times below the results it then pages through, so while it
// announces another page the total is raised to reach into it; on the last
// page it is the offset plus the results Spotify returned there, those it
// could not show included. It never exceeds the results pathfinder pages
// through.
func searchTotal(estimate int, paging pathfinder.PagingInfo, offset, n int) int {
	if paging.NextOffset == nil {
		return offset + n
	}
	return min(max(estimate, *paging.NextOffset+1), searchMaxResults)
}

func searchPageTotal(p *pathfinder.SearchPage, offset int) int {
	return searchTotal(p.TotalCount, p.PagingInfo, offset, len(p.Items))
}

// searchItems maps a page of results, skipping whatever is not of the page's
// type: an error wrapper (NotFound, GenericError…), a null, or a result
// without a URI. One odd result never costs the others.
func searchItems[T any](wrappers []pathfinder.EntityWrapper, item func(pathfinder.Entity) (T, bool)) []T {
	out := make([]T, 0, len(wrappers))
	for _, w := range wrappers {
		if v, ok := item(w.Data); ok {
			out = append(out, v)
		}
	}
	return out
}

// searchEntity decodes a result as a T, if it is one.
func searchEntity[T any](e pathfinder.Entity) (T, bool) {
	v, err := e.ValueByDiscriminator()
	t, ok := v.(T)
	return t, ok && tolerable(err)
}

func searchTrackItem(imageUrl searchImageUrlFunc) func(pathfinder.Entity) (ApiSearchTrack, bool) {
	return func(e pathfinder.Entity) (ApiSearchTrack, bool) {
		v, ok := searchEntity[pathfinder.Track](e)
		return searchTrack(v, imageUrl), ok && v.Uri != ""
	}
}

func searchTrack(v pathfinder.Track, imageUrl searchImageUrlFunc) ApiSearchTrack {
	out := ApiSearchTrack{
		Uri:           v.Uri,
		Name:          v.Name,
		ArtistNames:   make([]string, 0, len(v.Artists.Items)),
		ArtistUris:    make([]string, 0, len(v.Artists.Items)),
		AlbumName:     v.AlbumOfTrack.Name,
		AlbumUri:      v.AlbumOfTrack.Uri,
		AlbumCoverUrl: imageUrl(v.AlbumOfTrack.CoverArt),
		Duration:      v.Duration.TotalMilliseconds,
	}
	for _, a := range v.Artists.Items {
		out.ArtistNames = append(out.ArtistNames, a.Profile.Name)
		out.ArtistUris = append(out.ArtistUris, a.Uri)
	}
	return out
}

func searchEpisodeItem(imageUrl searchImageUrlFunc) func(pathfinder.Entity) (ApiSearchTrack, bool) {
	return func(e pathfinder.Entity) (ApiSearchTrack, bool) {
		v, ok := searchEntity[pathfinder.Episode](e)
		return searchEpisode(v, imageUrl), ok && v.Uri != ""
	}
}

// searchEpisode describes an episode as the track schema does: its show stands
// for the album and the artists.
func searchEpisode(v pathfinder.Episode, imageUrl searchImageUrlFunc) ApiSearchTrack {
	show := v.PodcastV2.Data
	out := ApiSearchTrack{
		Uri:           v.Uri,
		Name:          v.Name,
		ArtistNames:   []string{},
		ArtistUris:    []string{},
		AlbumName:     show.Name,
		AlbumUri:      show.Uri,
		AlbumCoverUrl: imageUrl(v.CoverArt),
		Duration:      v.Duration.TotalMilliseconds,
	}
	if show.Name != "" {
		out.ArtistNames = append(out.ArtistNames, show.Name)
	}
	return out
}

func searchAlbumItem(imageUrl searchImageUrlFunc) func(pathfinder.Entity) (ApiLibraryAlbum, bool) {
	return func(e pathfinder.Entity) (ApiLibraryAlbum, bool) {
		v, ok := searchEntity[pathfinder.Album](e)
		return searchAlbum(v, imageUrl), ok && v.Uri != ""
	}
}

func searchAlbum(v pathfinder.Album, imageUrl searchImageUrlFunc) ApiLibraryAlbum {
	out := ApiLibraryAlbum{
		Uri:         v.Uri,
		Name:        v.Name,
		ArtistNames: make([]string, 0, len(v.Artists.Items)),
		Year:        v.Date.Year,
		ImageUrl:    imageUrl(v.CoverArt),
	}
	for _, a := range v.Artists.Items {
		out.ArtistNames = append(out.ArtistNames, a.Profile.Name)
	}
	return out
}

func searchArtistItem(imageUrl searchImageUrlFunc) func(pathfinder.Entity) (ApiLibraryArtist, bool) {
	return func(e pathfinder.Entity) (ApiLibraryArtist, bool) {
		v, ok := searchEntity[pathfinder.Artist](e)
		return searchArtist(v, imageUrl), ok && v.Uri != ""
	}
}

func searchArtist(v pathfinder.Artist, imageUrl searchImageUrlFunc) ApiLibraryArtist {
	return ApiLibraryArtist{Uri: v.Uri, Name: v.Profile.Name, ImageUrl: imageUrl(v.Visuals.AvatarImage)}
}

func searchPlaylistItem(imageUrl searchImageUrlFunc) func(pathfinder.Entity) (ApiSearchPlaylist, bool) {
	return func(e pathfinder.Entity) (ApiSearchPlaylist, bool) {
		v, ok := searchEntity[pathfinder.Playlist](e)
		return searchPlaylist(v, imageUrl), ok && v.Uri != ""
	}
}

func searchPlaylist(v pathfinder.Playlist, imageUrl searchImageUrlFunc) ApiSearchPlaylist {
	out := ApiSearchPlaylist{
		Uri:           v.Uri,
		Name:          v.Name,
		Description:   v.Description,
		OwnerUsername: v.OwnerV2.Data.Username,
	}
	if len(v.Images.Items) > 0 {
		out.ImageUrl = imageUrl(v.Images.Items[0])
	}
	return out
}

func searchShowItem(imageUrl searchImageUrlFunc) func(pathfinder.Entity) (ApiSearchShow, bool) {
	return func(e pathfinder.Entity) (ApiSearchShow, bool) {
		v, ok := searchEntity[pathfinder.Podcast](e)
		return searchShow(v, imageUrl), ok && v.Uri != ""
	}
}

func searchShow(v pathfinder.Podcast, imageUrl searchImageUrlFunc) ApiSearchShow {
	return ApiSearchShow{Uri: v.Uri, Name: v.Name, Publisher: v.Publisher.Name, ImageUrl: imageUrl(v.CoverArt)}
}

// autoCompleteTypename is the wrapper of a suggested query in searchSuggestions.
const autoCompleteTypename = "SearchAutoCompleteEntity"

// searchSuggestions sorts the suggestions by type, each list in Spotify's
// order.
func searchSuggestions(hits []pathfinder.SearchHit, imageUrl searchImageUrlFunc) *ApiSearchSuggestions {
	out := &ApiSearchSuggestions{
		Queries:   []string{},
		Tracks:    []ApiSearchTrack{},
		Albums:    []ApiLibraryAlbum{},
		Artists:   []ApiLibraryArtist{},
		Playlists: []ApiSearchPlaylist{},
		Shows:     []ApiSearchShow{},
		Episodes:  []ApiSearchTrack{},
	}

	for _, hit := range hits {
		if hit.Item.Typename == autoCompleteTypename {
			if text := suggestedQuery(hit.Item.Data); text != "" {
				out.Queries = append(out.Queries, text)
			}
			continue
		}

		v, err := hit.Item.Data.ValueByDiscriminator()
		if !tolerable(err) {
			continue
		}
		switch v := v.(type) {
		case pathfinder.Track:
			if v.Uri != "" {
				out.Tracks = append(out.Tracks, searchTrack(v, imageUrl))
			}
		case pathfinder.Album:
			if v.Uri != "" {
				out.Albums = append(out.Albums, searchAlbum(v, imageUrl))
			}
		case pathfinder.Artist:
			if v.Uri != "" {
				out.Artists = append(out.Artists, searchArtist(v, imageUrl))
			}
		case pathfinder.Playlist:
			if v.Uri != "" {
				out.Playlists = append(out.Playlists, searchPlaylist(v, imageUrl))
			}
		case pathfinder.Podcast:
			if v.Uri != "" {
				out.Shows = append(out.Shows, searchShow(v, imageUrl))
			}
		case pathfinder.Episode:
			if v.Uri != "" {
				out.Episodes = append(out.Episodes, searchEpisode(v, imageUrl))
			}
		}
	}

	return out
}

// suggestedQuery returns the text of a suggested query, empty if it has none.
// Its wrapper's data is not one of the results Entity tells apart, so it is
// read again from the raw JSON.
func suggestedQuery(data pathfinder.Entity) string {
	raw, err := data.MarshalJSON()
	if err != nil {
		return ""
	}

	var c pathfinder.AutoCompleteEntity
	if !tolerable(json.Unmarshal(raw, &c)) {
		return ""
	}
	return c.Text
}
