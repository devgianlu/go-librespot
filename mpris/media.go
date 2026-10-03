package mpris

import (
	"encoding/hex"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/proto/spotify/metadata"
)

// What the media servers of every platform tell about the playing media, so
// they show the same.

func last[T any](a []T) T {
	return a[len(a)-1]
}

func coverArtUrl(fileId []uint8) string {
	return "https://i.scdn.co/image/" + hex.EncodeToString(fileId)
}

// mediaCoverUrl returns the URL of the largest cover of a track's album or an
// episode's show, or "" when there is none.
func mediaCoverUrl(media *librespot.Media) string {
	var images []*metadata.Image
	switch {
	case media == nil:
		return ""
	case media.IsTrack():
		images = media.Track().GetAlbum().GetCoverGroup().GetImage()
	case media.IsEpisode():
		images = media.Episode().GetShow().GetCoverImage().GetImage()
	}
	if len(images) == 0 {
		return ""
	}
	return coverArtUrl(last(images).GetFileId())
}

func artistsNames(artists []*metadata.Artist) []string {
	names := make([]string, len(artists))
	for i, a := range artists {
		names[i] = a.GetName()
	}
	return names
}
