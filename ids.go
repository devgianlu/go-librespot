package go_librespot

import (
	"encoding/hex"
	"fmt"
	connectpb "go-librespot/proto/spotify/connectstate/model"
	"math/big"
	"regexp"
	"strings"
)

var UriRegexp = regexp.MustCompile("^spotify:([a-z]+):([0-9a-zA-Z]{21,22})$")

func ContextTrackToProvidedTrack(track *connectpb.ContextTrack) *connectpb.ProvidedTrack {
	var uri string
	if len(track.Gid) > 0 {
		uri = TrackId(track.Gid).Uri()
	} else if len(track.Uri) > 0 {
		uri = track.Uri
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

type TrackId []byte

func (id TrackId) Hex() string {
	return hex.EncodeToString(id)
}

func (id TrackId) Base62() string {
	s := new(big.Int).SetBytes(id).Text(62)
	return strings.Repeat("0", 22-len(s)) + s
}

func (id TrackId) Uri() string {
	return fmt.Sprintf("spotify:track:%s", id.Base62())
}

func TrackIdFromUri(uri string) TrackId {
	matches := UriRegexp.FindStringSubmatch(uri)
	if len(matches) == 0 {
		panic(fmt.Sprintf("invalid uri: %s", uri))
	} else if matches[1] != "track" {
		panic(fmt.Sprintf("invalid uri type for track: %s", matches[1]))
	}

	var i big.Int
	_, ok := i.SetString(matches[2], 62)
	if !ok {
		panic("failed decoding base62 track uri")
	}

	return i.FillBytes(make([]byte, 16))
}
