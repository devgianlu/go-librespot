package daemon

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"github.com/devgianlu/go-librespot/spclient"
)

// libraryPlaylistsTimeout bounds fetching the rootlist for an API caller.
const libraryPlaylistsTimeout = 30 * time.Second

// libraryRequestTimeout bounds a request to Liked Songs or a playlist.
const libraryRequestTimeout = 30 * time.Second

// rootlistPageSize is how many rootlist entries are asked for per request.
// Folder markers count as entries, so a library needs a few more requests
// than its playlist count alone would suggest.
const rootlistPageSize = 500

// rootlistMaxPages caps the requests made for one listing, in case the
// service keeps reporting a truncated page without advancing.
const rootlistMaxPages = 20

const (
	rootlistStartGroupPrefix = "spotify:start-group:"
	rootlistEndGroupPrefix   = "spotify:end-group:"
	imageUrlPrefix           = "https://i.scdn.co/image/"
)

// fetchLibraryPlaylists fetches the whole rootlist and flattens it into the
// user's playlists, in library order.
func fetchLibraryPlaylists(ctx context.Context, spc *spclient.Spclient, username string) ([]ApiLibraryPlaylist, error) {
	var items []*playlist4pb.Item
	var metas []*playlist4pb.MetaItem
	for page := 0; page < rootlistMaxPages; page++ {
		list, err := spc.Rootlist(ctx, username, len(items), rootlistPageSize)
		if err != nil {
			return nil, err
		}

		contents := list.GetContents()
		pageItems := contents.GetItems()
		if len(contents.GetMetaItems()) != len(pageItems) {
			return nil, fmt.Errorf("rootlist page has %d items but %d meta items", len(pageItems), len(contents.GetMetaItems()))
		}

		items = append(items, pageItems...)
		metas = append(metas, contents.GetMetaItems()...)
		if !contents.GetTruncated() || len(pageItems) == 0 {
			return flattenRootlist(items, metas), nil
		}
	}

	return nil, fmt.Errorf("rootlist still truncated after %d pages", rootlistMaxPages)
}

// flattenRootlist turns rootlist entries into playlists, replacing the folder
// markers around them with each playlist's folder path. items and metas are
// parallel slices.
func flattenRootlist(items []*playlist4pb.Item, metas []*playlist4pb.MetaItem) []ApiLibraryPlaylist {
	playlists := make([]ApiLibraryPlaylist, 0, len(items))
	var folders []string
	for i, item := range items {
		uri := item.GetUri()
		switch {
		case strings.HasPrefix(uri, rootlistStartGroupPrefix):
			folders = append(folders, rootlistFolderName(uri))
		case strings.HasPrefix(uri, rootlistEndGroupPrefix):
			if len(folders) > 0 {
				folders = folders[:len(folders)-1]
			}
		case strings.HasPrefix(uri, "spotify:playlist:"):
			playlists = append(playlists, libraryPlaylist(uri, metas[i], folders))
		}
	}

	return playlists
}

// rootlistFolderName extracts the name from a start-group marker of the form
// spotify:start-group:<id>:<name>, where the name is form-encoded.
func rootlistFolderName(uri string) string {
	_, rest, _ := strings.Cut(strings.TrimPrefix(uri, rootlistStartGroupPrefix), ":")
	name, err := url.QueryUnescape(rest)
	if err != nil {
		return rest
	}

	return name
}

func libraryPlaylist(uri string, meta *playlist4pb.MetaItem, folders []string) ApiLibraryPlaylist {
	attrs := meta.GetAttributes()
	return ApiLibraryPlaylist{
		Uri:           uri,
		Name:          attrs.GetName(),
		Description:   attrs.GetDescription(),
		OwnerUsername: meta.GetOwnerUsername(),
		Length:        meta.GetLength(),
		ImageUrl:      playlistImageUrl(attrs),
		Collaborative: attrs.GetCollaborative(),
		CanEdit:       meta.GetCapabilities().GetCanEditItems(),
		Folder:        append([]string{}, folders...),
	}
}

// playlistImageUrl prefers the service's own picture URLs, "default" first,
// and falls back to building one from the picture file ID.
func playlistImageUrl(attrs *playlist4pb.ListAttributes) *string {
	var first string
	for _, size := range attrs.GetPictureSize() {
		if size.GetUrl() == "" {
			continue
		}
		if size.GetTargetName() == "default" {
			u := size.GetUrl()
			return &u
		}
		if first == "" {
			first = size.GetUrl()
		}
	}
	if first != "" {
		return &first
	}

	if picture := attrs.GetPicture(); len(picture) > 0 {
		u := imageUrlPrefix + hex.EncodeToString(picture)
		return &u
	}

	return nil
}

// pageLibraryPlaylists slices one page out of the flattened playlists.
func pageLibraryPlaylists(playlists []ApiLibraryPlaylist, offset, limit int) *ApiLibraryPlaylists {
	start := min(offset, len(playlists))
	end := min(start+limit, len(playlists))
	return &ApiLibraryPlaylists{
		Total:  len(playlists),
		Offset: offset,
		Limit:  limit,
		Items:  playlists[start:end],
	}
}

// appendToPlaylist appends uris to a playlist. The change has to name the
// revision it is based on; if the playlist moves on in between, it is read
// again and the append retried once.
func appendToPlaylist(ctx context.Context, spc *spclient.Spclient, username, playlistUri string, uris []string) error {
	id, err := librespot.SpotifyIdFromUri(playlistUri)
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		revision, err := spc.PlaylistRevision(ctx, *id)
		if err != nil {
			return err
		}

		err = spc.PlaylistAppend(ctx, *id, username, revision, uris)
		if !errors.Is(err, spclient.ErrPlaylistConflict) || attempt > 0 {
			return err
		}
	}
}

// libraryError wraps a failed library request, carrying a 403 or 404 from
// Spotify over as ErrForbidden or ErrNotFound: a playlist the user may not
// edit, or one that is gone, is the client's to handle, not a daemon fault.
func libraryError(what string, err error) error {
	var status *spclient.StatusError
	if errors.As(err, &status) {
		switch status.StatusCode {
		case http.StatusForbidden:
			return fmt.Errorf("%s: %w: %w", what, ErrForbidden, err)
		case http.StatusNotFound:
			return fmt.Errorf("%s: %w: %w", what, ErrNotFound, err)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// errPlaylistItemMoved reports that the entry a removal names is no longer
// there; the client's listing is out of date.
var errPlaylistItemMoved = fmt.Errorf("%w: the item is no longer at that position", spclient.ErrPlaylistConflict)

// itemPosition finds the entry to remove: the one at position, which must
// still be uri, or without a position the first occurrence of uri.
func itemPosition(uris []string, uri string, position *int) (int, error) {
	if position != nil {
		if *position < len(uris) && uris[*position] == uri {
			return *position, nil
		}
		return 0, errPlaylistItemMoved
	}
	for i, u := range uris {
		if u == uri {
			return i, nil
		}
	}
	return 0, ErrNotFound
}

// removeFromPlaylist removes one entry of uri from a playlist. The change
// names the revision it is based on; if the playlist moves on in between, it
// is read again and the removal retried once.
func removeFromPlaylist(ctx context.Context, spc *spclient.Spclient, username, playlistUri, uri string, position *int) error {
	id, err := librespot.SpotifyIdFromUri(playlistUri)
	if err != nil {
		return err
	}

	for attempt := 0; ; attempt++ {
		revision, uris, err := spc.PlaylistContents(ctx, *id)
		if err == nil {
			var index int
			if index, err = itemPosition(uris, uri, position); err != nil {
				return err
			}
			err = spc.PlaylistRemove(ctx, *id, username, revision, uri, index)
		}
		if !errors.Is(err, spclient.ErrPlaylistConflict) || attempt > 0 {
			return err
		}
	}
}

// playlistContains tells for each of uris whether the playlist holds it.
func playlistContains(ctx context.Context, spc *spclient.Spclient, playlistUri string, uris []string) ([]ApiPlaylistContainsState, error) {
	id, err := librespot.SpotifyIdFromUri(playlistUri)
	if err != nil {
		return nil, err
	}
	_, items, err := spc.PlaylistContents(ctx, *id)
	if err != nil {
		return nil, err
	}

	held := make(map[string]bool, len(items))
	for _, item := range items {
		held[item] = true
	}
	states := make([]ApiPlaylistContainsState, len(uris))
	for i, uri := range uris {
		states[i] = ApiPlaylistContainsState{Uri: uri, Contained: held[uri]}
	}
	return states, nil
}
