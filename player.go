package go_librespot

import (
	"errors"
	metadatapb "go-librespot/proto/spotify/metadata"
)

var ErrMediaRestricted = errors.New("media is restricted")

type Media struct {
	track   *metadatapb.Track
	episode *metadatapb.Episode
}

func NewMediaFromTrack(track *metadatapb.Track) *Media {
	if track == nil {
		panic("nil track")
	}

	return &Media{track: track, episode: nil}
}

func NewMediaFromEpisode(episode *metadatapb.Episode) *Media {
	if episode == nil {
		panic("nil episode")
	}

	return &Media{track: nil, episode: episode}
}

func (te Media) IsTrack() bool {
	return te.track != nil
}

func (te Media) IsEpisode() bool {
	return te.episode != nil
}

func (te Media) Track() *metadatapb.Track {
	if te.track == nil {
		panic("not a track")
	}

	return te.track
}

func (te Media) Episode() *metadatapb.Episode {
	if te.episode == nil {
		panic("not an episode")
	}

	return te.episode
}

func (te Media) Name() string {
	if te.track != nil {
		return *te.track.Name
	} else {
		return *te.episode.Name
	}
}

func (te Media) Duration() int32 {
	if te.track != nil {
		return *te.track.Duration
	} else {
		return *te.episode.Duration
	}
}

func (te Media) Restriction() []*metadatapb.Restriction {
	if te.track != nil {
		return te.track.Restriction
	} else {
		return te.episode.Restriction
	}
}
