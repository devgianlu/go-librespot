package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"github.com/devgianlu/go-librespot/spclient"
)

// libraryPlaylistsTimeout bounds fetching the rootlist for an API caller.
const libraryPlaylistsTimeout = 30 * time.Second

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
