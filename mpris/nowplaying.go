package mpris

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// artworkTimeout bounds downloading a cover for the Now Playing widget.
	artworkTimeout = 15 * time.Second
	// artworkMaxBytes bounds a downloaded cover.
	artworkMaxBytes = 4 << 20
)

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

// nowPlayingInfoFrom describes a daemon state for macOS. Stopped, or without
// media, it is stopped and empty, so the widget is cleared.
func nowPlayingInfoFrom(state MediaState) nowPlayingInfo {
	m := state.Media
	if state.PlaybackStatus == Stopped || m == nil {
		return nowPlayingInfo{state: nowPlayingStopped}
	}

	info := nowPlayingInfo{
		positionSec: float64(state.PositionMs) / 1000,
		artUrl:      mediaCoverUrl(m),
		state:       nowPlayingPlaying,
	}
	if state.PlaybackStatus == Paused {
		info.state = nowPlayingPaused
	}
	if state.Uri != nil {
		info.key = *state.Uri
	}

	switch {
	case m.IsTrack():
		t := m.Track()
		info.title, info.album = t.GetName(), t.GetAlbum().GetName()
		info.artist = strings.Join(artistsNames(t.GetArtist()), ", ")
		info.durationSec = float64(t.GetDuration()) / 1000
	case m.IsEpisode():
		e := m.Episode()
		info.title, info.artist, info.album = e.GetName(), e.GetShow().GetName(), e.GetShow().GetName()
		info.durationSec = float64(e.GetDuration()) / 1000
	}
	return info
}

// errArtworkTooLarge reports a cover larger than the widget takes.
var errArtworkTooLarge = errors.New("cover too large")

// downloadArtwork downloads a cover of at most maxBytes.
func downloadArtwork(ctx context.Context, client *http.Client, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("invalid status code from cover: %d", resp.StatusCode)
	}

	// One byte more tells a cover of exactly maxBytes from a cut off one.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errArtworkTooLarge
	}
	return data, nil
}

// artworkLoader fetches the cover of the track the widget shows. It follows
// the key last sent to the widget, which drops its cover whenever the key
// changes: a new key cancels the download for the old one, and a cover is
// fetched again until one arrives for the current key.
type artworkLoader struct {
	// download fetches a cover; it must give up once ctx is done, which is
	// at the latest after artworkTimeout.
	download func(ctx context.Context, url string) ([]byte, error)
	// deliver hands a cover for key to the widget.
	deliver func(key string, data []byte)
	// failed reports a download that failed for another reason than a newer key.
	failed func(key string, err error)

	mu       sync.Mutex
	key      string             // key last sent to the widget
	loaded   bool               // the cover for key was delivered
	fetching context.CancelFunc // cancels the running download for key, if any
	closed   bool
}

// update follows the widget to key, whose cover is at url, if it has one.
func (l *artworkLoader) update(key, url string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if key != l.key {
		l.key, l.loaded = key, false
		l.cancelLocked()
	}
	if l.closed || l.loaded || l.fetching != nil || url == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), artworkTimeout)
	l.fetching = cancel
	go l.fetch(ctx, key, url)
}

func (l *artworkLoader) fetch(ctx context.Context, key, url string) {
	data, err := l.download(ctx, url)

	l.mu.Lock()
	defer l.mu.Unlock()
	if ctx.Err() == context.Canceled || key != l.key {
		return // the widget moved on; whoever cancelled took over
	}
	l.cancelLocked()
	if err != nil {
		// Not loaded, so the next update for key tries again.
		l.failed(key, err)
		return
	}
	l.loaded = true
	l.deliver(key, data)
}

func (l *artworkLoader) cancelLocked() {
	if l.fetching != nil {
		l.fetching()
		l.fetching = nil
	}
}

func (l *artworkLoader) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.cancelLocked()
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
