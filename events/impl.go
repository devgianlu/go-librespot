package events

import (
	"github.com/devgianlu/go-librespot/events/impl"
	"github.com/devgianlu/go-librespot/events/plugin"
)

var Plugin plugin.Interface = impl.Impl{}
