//go:build test_unit

package daemon

import (
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
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
	require.Equal(t, []string{""}, track.ArtistNames, "kept, in step with artist_uris")
	require.Equal(t, 0, track.TrackNumber)

	episode := p.newApiResponseStatusMedia(librespot.NewMediaFromEpisode(&metadatapb.Episode{
		Name: proto.String("Episode"),
	}), 0)
	require.Equal(t, "Episode", episode.Name)
	require.Equal(t, "", episode.Uri)
	require.Equal(t, []string{}, episode.ArtistNames, "no show, no artist rather than a blank one")

	nameless := p.newApiResponseStatusMedia(librespot.NewMediaFromEpisode(&metadatapb.Episode{
		Name: proto.String("Episode"),
		Show: &metadatapb.Show{Gid: gid(0x04)},
	}), 0)
	require.Equal(t, []string{}, nameless.ArtistNames)
	require.Equal(t, librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeShow, gid(0x04)).Uri(), nameless.AlbumUri)

	// No artists: still an array, as the spec declares artist_names.
	alone := metaTrack(gid(0x02), "Alone")
	alone.Artist = nil
	require.Equal(t, []string{}, p.newApiResponseStatusMedia(librespot.NewMediaFromTrack(alone), 0).ArtistNames)
}

func TestGidUriToleratesMalformedGids(t *testing.T) {
	require.Equal(t, "", gidUri(librespot.SpotifyIdTypeAlbum, nil))
	require.Equal(t, "", gidUri(librespot.SpotifyIdTypeAlbum, []byte{1, 2, 3}))
}

// A track unavailable in the user's country plays as a relinked alternative
// whose metadata carries another gid. Everywhere the API describes it, it keeps
// the uri the context and the library name it by, so a client can find the
// playing track in the list it shows.
func TestApiTrackKeepsTheUriOfARelinkedTrack(t *testing.T) {
	p := newMetaTestPlayer(t, echoMetadata, nil)
	requested, alternative := trackUri(0x01), librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeTrack, gid(0x02))
	relinked := librespot.NewMediaFromTrack(metaTrack(alternative.Id(), "Relinked"))

	id, err := librespot.SpotifyIdFromUri(requested)
	require.NoError(t, err)
	stream := &player.Stream{RequestedId: *id, Media: relinked}
	require.Equal(t, requested, p.newApiResponseStatusTrack(stream, 0).Uri, "status and metadata events")

	p.app.metaCache.putStream(requested, relinked)
	list := p.contextTracksResponse("spotify:playlist:37i9dQZF1EQpj7X7UK8OOF", []string{requested}, true)
	require.Equal(t, requested, list.Tracks[0].Track.Uri, "context tracks")

	require.Equal(t, alternative.Uri(), p.newApiResponseStatusMediaAs("", relinked, 0).Uri, "without a requested uri the media's own stays")
	require.Equal(t, alternative.Uri(), p.newApiResponseStatusTrack(&player.Stream{Media: relinked}, 0).Uri, "a stream without a requested id")
}
