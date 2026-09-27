//go:build test_unit

package daemon

import (
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestApiTrackCarriesAlbumAndArtistUris(t *testing.T) {
	p := &AppPlayer{app: &App{cfg: &Config{}}, prodInfo: &ProductInfo{}}

	track := p.newApiResponseStatusMedia(mediaFixture("x"), 0)
	require.Equal(t, librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeAlbum, gid(0xa1)).Uri(), track.AlbumUri)
	require.Equal(t, []string{librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeArtist, gid(0xa2)).Uri()}, track.ArtistUris)

	episode := p.newApiResponseStatusMedia(librespot.NewMediaFromEpisode(&metadatapb.Episode{
		Gid:        gid(0x03),
		Name:       proto.String("Episode"),
		Duration:   proto.Int32(1000),
		Show:       &metadatapb.Show{Gid: gid(0x04), Name: proto.String("Show")},
		CoverImage: &metadatapb.ImageGroup{},
	}), 0)
	require.Equal(t, librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeShow, gid(0x04)).Uri(), episode.AlbumUri, "episodes name their show, like album_name")
	require.NotNil(t, episode.ArtistUris, "artist_uris must serialise as []")
	require.Empty(t, episode.ArtistUris)
}

func TestGidUriToleratesMalformedGids(t *testing.T) {
	require.Equal(t, "", gidUri(librespot.SpotifyIdTypeAlbum, nil))
	require.Equal(t, "", gidUri(librespot.SpotifyIdTypeAlbum, []byte{1, 2, 3}))
}
