package go_librespot

import (
	"encoding/hex"
	"fmt"
	connectpb "go-librespot/proto/spotify/connectstate"
	"math/big"
	"regexp"
	"strings"
)

var UriRegexp = regexp.MustCompile("^spotify:([a-z]+):([0-9a-zA-Z]{21,22})$")

func InferSpotifyIdTypeFromContextUri(uri string) SpotifyIdType {
	if strings.HasPrefix(uri, "spotify:episode:") || strings.HasPrefix(uri, "spotify:show:") {
		return SpotifyIdTypeEpisode
	}

	return SpotifyIdTypeTrack
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

	return &connectpb.ProvidedTrack{
		Uri:       uri,
		Uid:       track.Uid,
		Metadata:  track.Metadata,
		ArtistUri: artistUri,
		AlbumUri:  albumUri,
		Provider:  "context",
	}
}

type SpotifyIdType string

const (
	SpotifyIdTypeTrack   SpotifyIdType = "track"
	SpotifyIdTypeEpisode SpotifyIdType = "episode"
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

func SpotifyIdFromUri(uri string) SpotifyId {
	matches := UriRegexp.FindStringSubmatch(uri)
	if len(matches) == 0 {
		panic(fmt.Sprintf("invalid uri: %s", uri))
	}

	var i big.Int
	_, ok := i.SetString(matches[2], 62)
	if !ok {
		panic("failed decoding base62 track uri")
	}

	return SpotifyId{SpotifyIdType(matches[1]), i.FillBytes(make([]byte, 16))}
}
