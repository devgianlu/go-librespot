//go:build !linux

package mpris

import (
	librespot "github.com/devgianlu/go-librespot"
)

// NewServer opens the dbus connection and registers everything important
//
//goland:noinspection GoUnusedParameter
func NewServer(logger librespot.Logger) (_ *DummyServer, err error) {
	logger.Warn("mpris was set to enabled although it is not included in this build")

	return &DummyServer{}, nil
}
