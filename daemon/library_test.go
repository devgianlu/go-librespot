//go:build test_unit

package daemon

import (
	"testing"

	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	playlist_permissionpb "github.com/devgianlu/go-librespot/proto/spotify/playlist_permission"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func rootlistEntry(uri string, meta *playlist4pb.MetaItem) (*playlist4pb.Item, *playlist4pb.MetaItem) {
	if meta == nil {
		meta = &playlist4pb.MetaItem{}
	}
	return &playlist4pb.Item{Uri: proto.String(uri)}, meta
}

func namedMeta(name string, length int32) *playlist4pb.MetaItem {
	return &playlist4pb.MetaItem{
		Attributes:    &playlist4pb.ListAttributes{Name: proto.String(name)},
		Length:        proto.Int32(length),
		OwnerUsername: proto.String("owner"),
	}
}

func TestFlattenRootlist(t *testing.T) {
	var items []*playlist4pb.Item
	var metas []*playlist4pb.MetaItem
	add := func(uri string, meta *playlist4pb.MetaItem) {
		item, meta := rootlistEntry(uri, meta)
		items, metas = append(items, item), append(metas, meta)
	}

	add("spotify:playlist:top", namedMeta("Top", 10))
	add("spotify:start-group:aaa:Party+%26+Fun", nil)
	add("spotify:playlist:outer", namedMeta("Outer", 20))
	add("spotify:start-group:bbb:Old%3A+80s", nil)
	add("spotify:playlist:inner", namedMeta("Inner", 30))
	add("spotify:end-group:bbb", nil)
	add("spotify:end-group:aaa", nil)
	add("spotify:playlist:last", namedMeta("Last", 40))

	playlists := flattenRootlist(items, metas)

	require.Len(t, playlists, 4)
	require.Equal(t, "spotify:playlist:top", playlists[0].Uri)
	require.Equal(t, "Top", playlists[0].Name)
	require.Equal(t, int32(10), playlists[0].Length)
	require.Equal(t, "owner", playlists[0].OwnerUsername)
	require.NotNil(t, playlists[0].Folder, "folder must serialise as [] rather than null")
	require.Empty(t, playlists[0].Folder)

	require.Equal(t, []string{"Party & Fun"}, playlists[1].Folder)
	require.Equal(t, []string{"Party & Fun", "Old: 80s"}, playlists[2].Folder)
	require.Empty(t, playlists[3].Folder)
}

func TestLibraryPlaylistCanEdit(t *testing.T) {
	editable := namedMeta("Mine", 1)
	editable.Capabilities = &playlist_permissionpb.Capabilities{CanEditItems: proto.Bool(true)}

	require.True(t, libraryPlaylist("spotify:playlist:mine", editable, nil).CanEdit)
	require.False(t, libraryPlaylist("spotify:playlist:theirs", namedMeta("Theirs", 1), nil).CanEdit)
}

func TestFlattenRootlistToleratesUnbalancedGroups(t *testing.T) {
	item, meta := rootlistEntry("spotify:end-group:zzz", nil)
	pl, plMeta := rootlistEntry("spotify:playlist:x", namedMeta("X", 1))

	playlists := flattenRootlist([]*playlist4pb.Item{item, pl}, []*playlist4pb.MetaItem{meta, plMeta})

	require.Len(t, playlists, 1)
	require.Empty(t, playlists[0].Folder)
}

func TestPlaylistImageUrl(t *testing.T) {
	t.Run("prefers the default picture size", func(t *testing.T) {
		url := playlistImageUrl(&playlist4pb.ListAttributes{PictureSize: []*playlist4pb.PictureSize{
			{TargetName: proto.String("large"), Url: proto.String("https://example.com/large")},
			{TargetName: proto.String("default"), Url: proto.String("https://example.com/default")},
		}})
		require.NotNil(t, url)
		require.Equal(t, "https://example.com/default", *url)
	})

	t.Run("falls back to the first picture size", func(t *testing.T) {
		url := playlistImageUrl(&playlist4pb.ListAttributes{PictureSize: []*playlist4pb.PictureSize{
			{TargetName: proto.String("small"), Url: proto.String("")},
			{TargetName: proto.String("large"), Url: proto.String("https://example.com/large")},
		}})
		require.NotNil(t, url)
		require.Equal(t, "https://example.com/large", *url)
	})

	t.Run("builds a URL from the picture id", func(t *testing.T) {
		url := playlistImageUrl(&playlist4pb.ListAttributes{Picture: []byte{0xab, 0x67, 0x01}})
		require.NotNil(t, url)
		require.Equal(t, "https://i.scdn.co/image/ab6701", *url)
	})

	t.Run("is null without a picture", func(t *testing.T) {
		require.Nil(t, playlistImageUrl(&playlist4pb.ListAttributes{}))
	})
}

func TestPageLibraryPlaylists(t *testing.T) {
	playlists := make([]ApiLibraryPlaylist, 5)
	for i := range playlists {
		playlists[i].Uri = string(rune('a' + i))
	}

	page := pageLibraryPlaylists(playlists, 1, 2)
	require.Equal(t, 5, page.Total)
	require.Equal(t, 1, page.Offset)
	require.Equal(t, 2, page.Limit)
	require.Equal(t, []string{"b", "c"}, uris(page.Items))

	page = pageLibraryPlaylists(playlists, 4, 50)
	require.Equal(t, []string{"e"}, uris(page.Items))

	page = pageLibraryPlaylists(playlists, 10, 50)
	require.NotNil(t, page.Items, "items must serialise as [] rather than null")
	require.Empty(t, page.Items)
}

func uris(playlists []ApiLibraryPlaylist) []string {
	out := make([]string, len(playlists))
	for i, p := range playlists {
		out[i] = p.Uri
	}
	return out
}

func TestLibraryErrorMapsStatuses(t *testing.T) {
	forbidden := libraryError("failed appending", &spclient.StatusError{Op: "playlist changes", StatusCode: 403})
	require.ErrorIs(t, forbidden, ErrForbidden)
	require.Contains(t, forbidden.Error(), "403", "the cause stays in the message")

	require.ErrorIs(t, libraryError("x", &spclient.StatusError{Op: "playlist", StatusCode: 404}), ErrNotFound)

	other := libraryError("x", &spclient.StatusError{Op: "playlist", StatusCode: 500})
	require.NotErrorIs(t, other, ErrForbidden)
	require.NotErrorIs(t, other, ErrNotFound)
	require.ErrorIs(t, libraryError("x", spclient.ErrPlaylistConflict), spclient.ErrPlaylistConflict)
}

func TestItemPosition(t *testing.T) {
	uris := []string{"spotify:track:a", "spotify:track:b", "spotify:track:a"}
	at := func(i int) *int { return &i }

	got, err := itemPosition(uris, "spotify:track:a", at(2))
	require.NoError(t, err)
	require.Equal(t, 2, got, "the named duplicate, not the first one")

	got, err = itemPosition(uris, "spotify:track:a", nil)
	require.NoError(t, err)
	require.Equal(t, 0, got, "without a position, the first occurrence")

	_, err = itemPosition(uris, "spotify:track:a", at(1))
	require.ErrorIs(t, err, spclient.ErrPlaylistConflict, "another item at the position: the listing is stale")
	_, err = itemPosition(uris, "spotify:track:a", at(7))
	require.ErrorIs(t, err, spclient.ErrPlaylistConflict, "a position past the end: the listing is stale")

	_, err = itemPosition(uris, "spotify:track:z", nil)
	require.ErrorIs(t, err, ErrNotFound)
}
