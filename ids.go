package go_librespot

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

var UriRegexp = regexp.MustCompile("^spotify:([a-z]+):([0-9a-zA-Z]{21,22})$")

// ContextUriType extracts the type segment of a context URI, that is the "album"
// of "spotify:album:xxx". User scoped URIs such as
// "spotify:user:someone:playlist:xxx" carry the username in that position, so
// for those the segment after it is the meaningful one.
//
// The type is the last segment for a user scoped context that needs no id of
// its own — Liked Songs is "spotify:user:someone:collection" — so four segments
// are enough, not five.
func ContextUriType(uri string) string {
	parts := strings.Split(uri, ":")
	if len(parts) < 3 || parts[0] != "spotify" {
		return ""
	}

	if parts[1] == "user" && len(parts) >= 4 {
		return parts[3]
	}

	return parts[1]
}

// InferSpotifyIdTypeFromContextUri returns the type of the *items* a context
// holds. A ContextTrack often carries only a gid, and a gid does not say what it
// identifies, so the context is what decides how to turn it back into a URI.
//
// Anything unrecognised returns SpotifyIdTypeUnknown instead of being assumed to
// be a track.
func InferSpotifyIdTypeFromContextUri(uri string) SpotifyIdType {
	switch ContextUriType(uri) {
	// Contexts made of podcast episodes or audiobook chapters.
	case "episode", "show", "chapter", "audiobook":
		return SpotifyIdTypeEpisode

	// Contexts made of tracks. "collection" is Liked Songs; its saved episodes
	// variant is handled below. Both come user scoped as well, as
	// spotify:user:someone:collection[:your-episodes].
	case "album", "artist", "playlist", "track", "station", "dailymix",
		"collection", "top", "trackset", "list", "local", "concept",
		"running", "genre", "radio", "folder":
		if strings.HasSuffix(uri, ":collection:your-episodes") {
			return SpotifyIdTypeEpisode
		}
		return SpotifyIdTypeTrack

	case "ad", "app", "audio", "audiofile", "author", "clip", "conceptclass",
		"concert", "image", "instance", "internal", "interruption", "licensor",
		"localfileimage", "media", "meta", "mosaic", "partner", "prerelease",
		"promotion", "room", "search", "socialsession", "suggest", "transition",
		"user", "userimage", "zerotap":
		return SpotifyIdTypeUnknown

	default:
		return SpotifyIdTypeUnknown
	}
}

func ContextTrackToProvidedTrack(typ SpotifyIdType, track *connectpb.ContextTrack) *connectpb.ProvidedTrack {
	var uri string
	if len(track.Uri) > 0 {
		uri = track.Uri
	} else if len(track.Gid) > 0 {
		uri = SpotifyIdFromGid(typ, track.Gid).Uri()
	} else {
		panic("invalid context track")
	}

	artistUri, _ := track.Metadata["artist_uri"]
	albumUri, _ := track.Metadata["album_uri"]

	provider := "context"
	if val := track.Metadata["is_queued"]; val == "true" {
		provider = "queue"
	} else if val = track.Metadata["autoplay.is_autoplay"]; val == "true" {
		provider = "autoplay"
	}

	return &connectpb.ProvidedTrack{
		Uri:       uri,
		Uid:       track.Uid,
		Metadata:  track.Metadata,
		ArtistUri: artistUri,
		AlbumUri:  albumUri,
		Provider:  provider,
	}
}

type SpotifyIdType string

const (
	SpotifyIdTypeTrack    SpotifyIdType = "track"
	SpotifyIdTypeEpisode  SpotifyIdType = "episode"
	SpotifyIdTypePlaylist SpotifyIdType = "playlist"
	SpotifyIdTypeAlbum    SpotifyIdType = "album"
	SpotifyIdTypeArtist   SpotifyIdType = "artist"
	SpotifyIdTypeShow     SpotifyIdType = "show"
	SpotifyIdTypeUnknown  SpotifyIdType = ""
)

type SpotifyId struct {
	typ SpotifyIdType
	id  []byte
}

func (id SpotifyId) Type() SpotifyIdType {
	return id.typ
}

func (id SpotifyId) Id() []byte {
	return id.id
}

func (id SpotifyId) Hex() string {
	return hex.EncodeToString(id.id)
}

func (id SpotifyId) Base62() string {
	return GidToBase62(id.id)
}

func (id SpotifyId) Uri() string {
	return fmt.Sprintf("spotify:%s:%s", id.Type(), id.Base62())
}

func (id SpotifyId) String() string {
	return id.Uri()
}

func GidToBase62(id []byte) string {
	s := new(big.Int).SetBytes(id).Text(62)
	return strings.Repeat("0", 22-len(s)) + s
}

func SpotifyIdFromGid(typ SpotifyIdType, id []byte) SpotifyId {
	if len(id) != 16 {
		panic(fmt.Sprintf("invalid gid: %s", hex.EncodeToString(id)))
	}

	return SpotifyId{typ, id}
}

func SpotifyIdFromBase62(typ SpotifyIdType, id string) (*SpotifyId, error) {
	var i big.Int
	_, ok := i.SetString(id, 62)
	if !ok {
		return nil, fmt.Errorf("failed decoding base62: %s", id)
	}

	return &SpotifyId{typ, i.FillBytes(make([]byte, 16))}, nil
}

func SpotifyIdFromUri(uri string) (_ *SpotifyId, err error) {
	matches := UriRegexp.FindStringSubmatch(uri)
	if len(matches) == 0 {
		return nil, fmt.Errorf("invalid uri: %s", uri)
	}

	return SpotifyIdFromBase62(SpotifyIdType(matches[1]), matches[2])
}
