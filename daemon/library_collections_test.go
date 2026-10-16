//go:build test_unit

package daemon

import (
	"testing"

	collectionpb "github.com/devgianlu/go-librespot/proto/spotify/collection/v2"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNewestUris(t *testing.T) {
	items := []*collectionpb.CollectionItem{
		{Uri: "spotify:album:old", AddedAt: 100},
		{Uri: "spotify:track:t", AddedAt: 500},
		{Uri: "spotify:album:new", AddedAt: 300},
		{Uri: "spotify:album:gone", AddedAt: 400, IsRemoved: true},
		{Uri: "spotify:album:tie", AddedAt: 100},
	}

	require.Equal(t, []string{"spotify:album:new", "spotify:album:old", "spotify:album:tie"}, newestUris(items, "spotify:album:"))
}

func TestPageBounds(t *testing.T) {
	for _, tc := range []struct{ n, offset, limit, start, end int }{
		{10, 0, 50, 0, 10},
		{10, 4, 3, 4, 7},
		{10, 10, 5, 10, 10},
		{10, 20, 5, 10, 10},
	} {
		start, end := pageBounds(tc.n, tc.offset, tc.limit)
		require.Equal(t, [2]int{tc.start, tc.end}, [2]int{start, end}, "%+v", tc)
	}
}

func fakeImageUrl(images []*metadatapb.Image) *string {
	if len(images) == 0 {
		return nil
	}
	url := "img:" + string(images[0].GetFileId())
	return &url
}

func TestLibraryAlbum(t *testing.T) {
	album := &metadatapb.Album{
		Name:   proto.String("Discovery"),
		Artist: []*metadatapb.Artist{{Name: proto.String("Daft Punk")}},
		Date:   &metadatapb.Date{Year: proto.Int32(2001)},
		CoverGroup: &metadatapb.ImageGroup{Image: []*metadatapb.Image{
			{FileId: []byte("c")},
		}},
	}

	got := libraryAlbum("spotify:album:a", album, fakeImageUrl)
	require.Equal(t, "Discovery", got.Name)
	require.Equal(t, []string{"Daft Punk"}, got.ArtistNames)
	require.Equal(t, 2001, got.Year)
	require.Equal(t, "img:c", *got.ImageUrl, "the cover group stands in for a missing cover")

	missing := libraryAlbum("spotify:album:b", nil, fakeImageUrl)
	require.Equal(t, "spotify:album:b", missing.Uri)
	require.NotNil(t, missing.ArtistNames, "artist_names must serialise as []")
	require.Nil(t, missing.ImageUrl)
}

func TestLibraryArtist(t *testing.T) {
	artist := &metadatapb.Artist{
		Name:     proto.String("Moloko"),
		Portrait: []*metadatapb.Image{{FileId: []byte("p")}},
	}

	got := libraryArtist("spotify:artist:a", artist, fakeImageUrl)
	require.Equal(t, "Moloko", got.Name)
	require.Equal(t, "img:p", *got.ImageUrl)

	require.Equal(t, ApiLibraryArtist{Uri: "spotify:artist:b"}, libraryArtist("spotify:artist:b", nil, fakeImageUrl))
}
