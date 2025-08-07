package impl

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/mercury"
	"github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/devgianlu/go-librespot/spclient"
)

func (p Impl) NewEventManager(librespot.Logger, *librespot.AppState, *mercury.Client, *spclient.Spclient, string) (player.EventManager, error) {
	return Impl{}, nil
}

type Impl struct{}

func (d Impl) PreStreamLoadNew([]byte, librespot.SpotifyId, int64)                               {}
func (d Impl) PostStreamResolveAudioFile([]byte, int32, *librespot.Media, *metadatapb.AudioFile) {}
func (d Impl) PostStreamRequestAudioKey([]byte)                                                  {}
func (d Impl) PostStreamResolveStorage([]byte)                                                   {}
func (d Impl) PostStreamInitHttpChunkReader([]byte, *audio.HttpChunkedReader)                    {}
func (d Impl) OnPrimaryStreamUnload(*player.Stream, int64)                                       {}
func (d Impl) PostPrimaryStreamLoad(*player.Stream, bool)                                        {}
func (d Impl) OnPlayerPlay(*player.Stream, string, bool, *connectpb.PlayOrigin, *connectpb.ProvidedTrack, int64) {
}
func (d Impl) OnPlayerResume(*player.Stream, int64) {}
func (d Impl) OnPlayerPause(*player.Stream, string, bool, *connectpb.PlayOrigin, *connectpb.ProvidedTrack, int64) {
}
func (d Impl) OnPlayerSeek(*player.Stream, int64, int64)       {}
func (d Impl) OnPlayerSkipForward(*player.Stream, int64, bool) {}
func (d Impl) OnPlayerSkipBackward(*player.Stream, int64)      {}
func (d Impl) OnPlayerEnd(*player.Stream, int64)               {}
func (d Impl) Close()                                          {}
