package plugin

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/mercury"
	"github.com/devgianlu/go-librespot/player"
	"github.com/devgianlu/go-librespot/spclient"
)

type Interface interface {
	NewEventManager(log librespot.Logger, state *librespot.AppState, hg *mercury.Client, sp *spclient.Spclient, username string) (player.EventManager, error)
}
