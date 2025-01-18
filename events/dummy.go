//go:build !events

package events

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/events/plugin"
	"github.com/devgianlu/go-librespot/spclient"
)

var Plugin plugin.Interface = dummyPlugin{}

type dummyPlugin struct {
}

func (p dummyPlugin) NewEventManager(librespot.Logger, *librespot.AppState, *spclient.Spclient, string) (plugin.EventManager, error) {
	return dummyEventManager{}, nil
}

type dummyEventManager struct {
}
