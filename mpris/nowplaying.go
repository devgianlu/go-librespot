package mpris

import (
	"encoding/hex"
	"strings"

	"github.com/devgianlu/go-librespot/proto/spotify/metadata"
)

func coverArtUrl(fileId []uint8) string {
	return "https://i.scdn.co/image/" + hex.EncodeToString(fileId)
}

// nowPlayingState is the playback state the macOS Now Playing widget shows.
type nowPlayingState int

const (
	nowPlayingStopped nowPlayingState = iota
	nowPlayingPlaying
	nowPlayingPaused
)

// nowPlayingInfo is what macOS shows about the playing track.
type nowPlayingInfo struct {
	key         string // identifies the track, so late artwork is not applied to the next one
	title       string
	artist      string
	album       string
	artUrl      string
	durationSec float64
	positionSec float64
	state       nowPlayingState
}

// nowPlayingInfoFrom describes a daemon state for macOS. Without media it is
// stopped and empty.
func nowPlayingInfoFrom(state MediaState) nowPlayingInfo {
	info := nowPlayingInfo{positionSec: float64(state.PositionMs) / 1000}
	switch state.PlaybackStatus {
	case Playing:
		info.state = nowPlayingPlaying
	case Paused:
		info.state = nowPlayingPaused
	}
	if state.Uri != nil {
		info.key = *state.Uri
	}

	m := state.Media
	switch {
	case m == nil:
		info.state = nowPlayingStopped
		info.positionSec = 0
	case m.IsTrack():
		t := m.Track()
		info.title, info.album = t.GetName(), t.GetAlbum().GetName()
		info.artist = joinArtists(t.GetArtist())
		info.durationSec = float64(t.GetDuration()) / 1000
		if images := t.GetAlbum().GetCoverGroup().GetImage(); len(images) > 0 {
			info.artUrl = coverArtUrl(images[len(images)-1].GetFileId())
		}
	case m.IsEpisode():
		e := m.Episode()
		info.title, info.artist, info.album = e.GetName(), e.GetShow().GetName(), e.GetShow().GetName()
		info.durationSec = float64(e.GetDuration()) / 1000
		if images := e.GetShow().GetCoverImage().GetImage(); len(images) > 0 {
			info.artUrl = coverArtUrl(images[len(images)-1].GetFileId())
		}
	}
	return info
}

func joinArtists(artists []*metadata.Artist) string {
	names := make([]string, 0, len(artists))
	for _, a := range artists {
		names = append(names, a.GetName())
	}
	return strings.Join(names, ", ")
}

// nowPlayingCommand is a command macOS hands us: a media key, a headphone
// button or a control of the Now Playing widget.
type nowPlayingCommand int

const (
	nowPlayingTogglePlayPause nowPlayingCommand = iota
	nowPlayingPlay
	nowPlayingPause
	nowPlayingNext
	nowPlayingPrevious
	nowPlayingStop
	nowPlayingSeek // to an absolute position, in seconds
)

var nowPlayingCommandNames = map[nowPlayingCommand]string{
	nowPlayingTogglePlayPause: "play/pause",
	nowPlayingPlay:            "play",
	nowPlayingPause:           "pause",
	nowPlayingNext:            "next",
	nowPlayingPrevious:        "previous",
	nowPlayingStop:            "stop",
	nowPlayingSeek:            "seek",
}

func (c nowPlayingCommand) String() string {
	if name, ok := nowPlayingCommandNames[c]; ok {
		return name
	}
	return "unknown"
}

// playerCommand translates a macOS command into the command the player
// takes from MPRIS. The reply is buffered, since nobody waits for it.
func (c nowPlayingCommand) playerCommand(positionSec float64) (MediaPlayer2PlayerCommand, bool) {
	cmd := MediaPlayer2PlayerCommand{response: make(chan MediaPlayer2PlayerCommandResponse, 1)}
	switch c {
	case nowPlayingTogglePlayPause:
		cmd.Type = MediaPlayer2PlayerCommandTypePlayPause
	case nowPlayingPlay:
		cmd.Type = MediaPlayer2PlayerCommandTypePlay
	case nowPlayingPause:
		cmd.Type = MediaPlayer2PlayerCommandTypePause
	case nowPlayingNext:
		cmd.Type = MediaPlayer2PlayerCommandTypeNext
	case nowPlayingPrevious:
		cmd.Type = MediaPlayer2PlayerCommandTypePrevious
	case nowPlayingStop:
		cmd.Type = MediaPlayer2PlayerCommandTypeStop
	case nowPlayingSeek:
		cmd.Type = MediaPlayer2PlayerCommandTypeSetPosition
		cmd.Argument = MediaPlayer2CommandSetPositionPayload{PositionUs: int64(positionSec * 1e6)}
	default:
		return MediaPlayer2PlayerCommand{}, false
	}
	return cmd, true
}
