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

// Cached metadata is not guaranteed complete: what it lacks is described empty,
// never dereferenced, so a gap in one track cannot take the status down.
func TestApiTrackToleratesIncompleteMetadata(t *testing.T) {
	p := &AppPlayer{app: &App{cfg: &Config{}}, prodInfo: &ProductInfo{}}

	incomplete := metaTrack(nil, "Track")
	incomplete.Album = nil
	incomplete.Artist = []*metadatapb.Artist{{Gid: gid(0xa2)}}
	incomplete.Number = nil
	track := p.newApiResponseStatusMedia(librespot.NewMediaFromTrack(incomplete), 0)
	require.Equal(t, "Track", track.Name)
	require.Equal(t, "", track.Uri)
	require.Equal(t, "", track.AlbumName)
	require.Equal(t, "", track.ReleaseDate)
	require.Equal(t, []string{""}, track.ArtistNames)
	require.Equal(t, 0, track.TrackNumber)

	episode := p.newApiResponseStatusMedia(librespot.NewMediaFromEpisode(&metadatapb.Episode{
		Name: proto.String("Episode"),
	}), 0)
	require.Equal(t, "Episode", episode.Name)
	require.Equal(t, "", episode.Uri)
	require.Equal(t, []string{""}, episode.ArtistNames)

	// No artists: still an array, as the spec declares artist_names.
	alone := metaTrack(gid(0x02), "Alone")
	alone.Artist = nil
	require.Equal(t, []string{}, p.newApiResponseStatusMedia(librespot.NewMediaFromTrack(alone), 0).ArtistNames)
}

func TestGidUriToleratesMalformedGids(t *testing.T) {
	require.Equal(t, "", gidUri(librespot.SpotifyIdTypeAlbum, nil))
	require.Equal(t, "", gidUri(librespot.SpotifyIdTypeAlbum, []byte{1, 2, 3}))
}
