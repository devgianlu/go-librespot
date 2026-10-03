//go:build darwin

package mpris

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework MediaPlayer
#include <stdlib.h>
#include "nowplaying_darwin.h"
*/
import "C"

import (
	"context"
	"net/http"
	"sync"
	"unsafe"

	librespot "github.com/devgianlu/go-librespot"
)

// NowPlayingServer is the macOS counterpart of the MPRIS server: it takes
// media keys, headphone buttons and the Now Playing widget's controls from
// MPRemoteCommandCenter and shows the playing track, with its cover, in
// MPNowPlayingInfoCenter. Both need RunWithEventLoop to run the main thread's
// event loop.
type NowPlayingServer struct {
	log      librespot.Logger
	commands chan MediaPlayer2PlayerCommand
	artwork  *artworkLoader
}

var (
	activeServerMu sync.Mutex
	activeServer   *NowPlayingServer
)

// NewServer creates the macOS Now Playing integration.
func NewServer(logger librespot.Logger) (*NowPlayingServer, error) {
	s := &NowPlayingServer{
		log:      logger,
		commands: make(chan MediaPlayer2PlayerCommand, 8),
	}
	s.artwork = &artworkLoader{
		download: func(ctx context.Context, url string) ([]byte, error) {
			return downloadArtwork(ctx, http.DefaultClient, url, artworkMaxBytes)
		},
		deliver: setArtwork,
		failed: func(key string, err error) {
			s.log.WithError(err).Debugf("now playing: failed downloading cover for %s", key)
		},
	}

	activeServerMu.Lock()
	activeServer = s
	activeServerMu.Unlock()

	C.nowPlayingSetup()
	return s, nil
}

//export goNowPlayingCommand
func goNowPlayingCommand(kind C.int, positionSec C.double) {
	activeServerMu.Lock()
	s := activeServer
	activeServerMu.Unlock()
	if s == nil {
		return
	}

	command := nowPlayingCommand(kind)
	cmd, ok := command.playerCommand(float64(positionSec))
	if !ok {
		return
	}
	s.log.Debugf("now playing: %s", command)

	// This runs on the main thread, which must not wait for the player.
	select {
	case s.commands <- cmd:
	default:
		s.log.Warnf("now playing: dropped %s, player busy", command)
	}
}

func (s *NowPlayingServer) EmitStateUpdate(state MediaState) {
	info := nowPlayingInfoFrom(state)

	cKey, cTitle, cArtist, cAlbum := C.CString(info.key), C.CString(info.title), C.CString(info.artist), C.CString(info.album)
	defer C.free(unsafe.Pointer(cKey))
	defer C.free(unsafe.Pointer(cTitle))
	defer C.free(unsafe.Pointer(cArtist))
	defer C.free(unsafe.Pointer(cAlbum))
	C.nowPlayingUpdate(cKey, cTitle, cArtist, cAlbum, C.double(info.durationSec), C.double(info.positionSec), C.int(info.state))

	s.artwork.update(info.key, info.artUrl)
}

// setArtwork hands a cover to the widget, for key's track.
func setArtwork(key string, data []byte) {
	cKey, cData := C.CString(key), C.CBytes(data)
	defer C.free(unsafe.Pointer(cKey))
	defer C.free(cData) // copied by nowPlayingSetArtwork
	C.nowPlayingSetArtwork(cKey, cData, C.int(len(data)))
}

func (s *NowPlayingServer) EmitSeekUpdate(state SeekState) {
	C.nowPlayingSetPosition(C.double(float64(state.PositionMs) / 1000))
}

func (s *NowPlayingServer) Receive() <-chan MediaPlayer2PlayerCommand {
	return s.commands
}

func (s *NowPlayingServer) Close() error {
	s.artwork.close()

	activeServerMu.Lock()
	if activeServer == s {
		activeServer = nil
	}
	activeServerMu.Unlock()
	return nil
}

// RunWithEventLoop runs fn while the calling goroutine, which must be the main
// goroutine locked to the main thread, runs the macOS event loop that remote
// commands are delivered through. It returns once fn has returned.
func RunWithEventLoop(fn func()) {
	go func() {
		defer C.nowPlayingStopRunLoop()
		fn()
	}()
	C.nowPlayingRunLoop()
}
