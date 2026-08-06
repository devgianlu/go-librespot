//go:build test_unit

package daemon

import (
	"net"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// gid builds a 16 byte gid, the only length SpotifyIdFromGid accepts.
func gid(b byte) []byte {
	out := make([]byte, 16)
	for i := range out {
		out[i] = b
	}
	return out
}

func image(size metadatapb.Image_Size, id byte) *metadatapb.Image {
	return &metadatapb.Image{Size: size.Enum(), FileId: []byte{id}}
}

func testTrackMedia() *librespot.Media {
	return librespot.NewMediaFromTrack(&metadatapb.Track{
		Gid:  gid(0x01),
		Name: proto.String("Don't Panic"),
		Album: &metadatapb.Album{
			Gid:  gid(0x02),
			Name: proto.String("Parachutes"),
			Cover: []*metadatapb.Image{
				image(metadatapb.Image_SMALL, 0xaa),
				image(metadatapb.Image_DEFAULT, 0xbb),
				image(metadatapb.Image_LARGE, 0xcc),
			},
		},
		Artist: []*metadatapb.Artist{{Gid: gid(0x03), Name: proto.String("Coldplay")}},
	})
}

func TestEnrichTrackMetadataTrack(t *testing.T) {
	provided := &connectpb.ProvidedTrack{Uri: "spotify:track:xxx", Provider: "context"}

	enrichTrackMetadata(provided, testTrackMedia())

	require.Equal(t, "Don't Panic", provided.Metadata["title"])
	require.Equal(t, "Parachutes", provided.Metadata["album_title"])
	require.Equal(t, librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeAlbum, gid(0x02)).Uri(),
		provided.Metadata["album_uri"])
	require.Equal(t, librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeArtist, gid(0x03)).Uri(),
		provided.Metadata["artist_uri"])

	// The top level fields track the metadata, as they would have if the
	// context resolver had supplied it.
	require.Equal(t, provided.Metadata["album_uri"], provided.AlbumUri)
	require.Equal(t, provided.Metadata["artist_uri"], provided.ArtistUri)

	// Spotify addresses artwork by image uri, not by CDN url.
	require.Equal(t, "spotify:image:aa", provided.Metadata["image_small_url"])
	require.Equal(t, "spotify:image:bb", provided.Metadata["image_url"])
	require.Equal(t, "spotify:image:cc", provided.Metadata["image_large_url"])

	// No xlarge cover exists, so it falls back to the closest one rather than
	// being left out — the official client does the same.
	require.Equal(t, "spotify:image:cc", provided.Metadata["image_xlarge_url"])
}

func TestEnrichTrackMetadataEpisode(t *testing.T) {
	provided := &connectpb.ProvidedTrack{Uri: "spotify:episode:xxx", Provider: "context"}

	enrichTrackMetadata(provided, librespot.NewMediaFromEpisode(&metadatapb.Episode{
		Gid:  gid(0x04),
		Name: proto.String("Sunday Pick"),
		Show: &metadatapb.Show{
			Gid:  gid(0x05),
			Name: proto.String("TED Talks Daily"),
		},
		CoverImage: &metadatapb.ImageGroup{
			Image: []*metadatapb.Image{image(metadatapb.Image_DEFAULT, 0xdd)},
		},
	}))

	require.Equal(t, "Sunday Pick", provided.Metadata["title"])

	// An episode has no album, so controllers are given the show in its place.
	require.Equal(t, "TED Talks Daily", provided.Metadata["album_title"])
	require.Equal(t, librespot.SpotifyIdFromGid(librespot.SpotifyIdTypeShow, gid(0x05)).Uri(),
		provided.Metadata["album_uri"])
	require.Equal(t, "spotify:image:dd", provided.Metadata["image_url"])

	// A show has no artist.
	require.NotContains(t, provided.Metadata, "artist_uri")
}

// The context resolver's metadata is what tells the daemon a track is queued or
// autoplayed, so enrichment must add to it rather than replace it.
func TestEnrichTrackMetadataKeepsContextMetadata(t *testing.T) {
	provided := &connectpb.ProvidedTrack{
		Uri:      "spotify:track:xxx",
		Metadata: map[string]string{"added_at": "1785470468548", "is_queued": "true"},
	}

	enrichTrackMetadata(provided, testTrackMedia())

	require.Equal(t, "1785470468548", provided.Metadata["added_at"])
	require.Equal(t, "true", provided.Metadata["is_queued"])
	require.Equal(t, "Don't Panic", provided.Metadata["title"])
}

// ContextTrackToProvidedTrack hands out the ContextTrack's own map, so writing
// into it would rewrite the track list under the player.
func TestEnrichTrackMetadataDoesNotMutateSharedMap(t *testing.T) {
	shared := map[string]string{"added_at": "1785470468548"}
	provided := &connectpb.ProvidedTrack{Uri: "spotify:track:xxx", Metadata: shared}

	enrichTrackMetadata(provided, testTrackMedia())

	require.Equal(t, map[string]string{"added_at": "1785470468548"}, shared,
		"the context track's metadata must be left alone")
	require.Contains(t, provided.Metadata, "title")
}

// A gid of the wrong length panics SpotifyIdFromGid, and metadata off the wire
// is not worth trusting that far.
func TestEnrichTrackMetadataToleratesMissingFields(t *testing.T) {
	provided := &connectpb.ProvidedTrack{Uri: "spotify:track:xxx"}

	require.NotPanics(t, func() {
		enrichTrackMetadata(provided, librespot.NewMediaFromTrack(&metadatapb.Track{
			Gid:    gid(0x01),
			Name:   proto.String("No Album"),
			Album:  &metadatapb.Album{Gid: []byte{0x01, 0x02}},
			Artist: []*metadatapb.Artist{{Gid: nil}},
		}))
	})

	require.Equal(t, "No Album", provided.Metadata["title"])
	require.NotContains(t, provided.Metadata, "album_uri")
	require.NotContains(t, provided.Metadata, "artist_uri")
	require.NotContains(t, provided.Metadata, "image_url")
}

func TestEnrichTrackMetadataIgnoresNilArguments(t *testing.T) {
	require.NotPanics(t, func() {
		enrichTrackMetadata(nil, testTrackMedia())
		enrichTrackMetadata(&connectpb.ProvidedTrack{}, nil)
	})
}

func TestDeviceAddressMask(t *testing.T) {
	mask := deviceAddressMask()
	if mask == "" {
		t.Skip("no usable ipv4 interface on this host")
	}

	ip, ipNet, err := net.ParseCIDR(mask)
	require.NoError(t, err, "must be valid CIDR")

	// The device's own address, not the network address: parsing the mask back
	// has to yield something other than the network it describes.
	require.NotNil(t, ip.To4(), "must be ipv4")
	require.False(t, ip.IsLoopback(), "must not be loopback")
	require.True(t, ipNet.Contains(ip))

	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)

	require.Contains(t, addrs, net.Addr(&net.IPNet{IP: ip, Mask: ipNet.Mask}),
		"must be an address actually assigned to an interface")
}
