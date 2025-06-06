//go:build !events

package events

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/events/plugin"
	"github.com/devgianlu/go-librespot/mercury"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/devgianlu/go-librespot/spclient"
)

var Plugin plugin.Interface = dummyPlugin{}

type dummyPlugin struct {
}

func (p dummyPlugin) NewEventManager(librespot.Logger, *librespot.AppState, *mercury.Client, *spclient.Spclient, string) (player.EventManager, error) {
	return dummyEventManager{}, nil
}

type dummyEventManager struct {
}

func (d dummyEventManager) PreStreamLoadNew([]byte, librespot.SpotifyId, int64) {
}

func (d dummyEventManager) PostStreamResolveAudioFile([]byte, int32, *librespot.Media, *metadatapb.AudioFile) {
}

func (d dummyEventManager) PostStreamRequestAudioKey([]byte) {
}

func (d dummyEventManager) PostStreamResolveStorage([]byte) {
}

func (d dummyEventManager) PostStreamInitHttpChunkReader([]byte, *audio.HttpChunkedReader) {
}

func (d dummyEventManager) OnPrimaryStreamUnload(*player.Stream, int64) {
}

func (d dummyEventManager) PostPrimaryStreamLoad(*player.Stream, bool) {
}

func (d dummyEventManager) OnPlayerPlay(*player.Stream, string, bool, *connectpb.PlayOrigin, *connectpb.ProvidedTrack, int64) {
}

func (d dummyEventManager) OnPlayerResume(*player.Stream, int64) {
}

func (d dummyEventManager) OnPlayerPause(*player.Stream, string, bool, *connectpb.PlayOrigin, *connectpb.ProvidedTrack, int64) {
}

func (d dummyEventManager) OnPlayerSeek(*player.Stream, int64, int64) {
}

func (d dummyEventManager) OnPlayerSkipForward(*player.Stream, int64, bool) {
}

func (d dummyEventManager) OnPlayerSkipBackward(*player.Stream, int64) {
}

func (d dummyEventManager) OnPlayerEnd(*player.Stream, int64) {
}

func (d dummyEventManager) Close() {
}
