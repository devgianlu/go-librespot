package plugin

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/player"
	"github.com/devgianlu/go-librespot/spclient"
)

type Interface interface {
	NewEventManager(log librespot.Logger, state *librespot.AppState, sp *spclient.Spclient, username string) (player.EventManager, error)
}
