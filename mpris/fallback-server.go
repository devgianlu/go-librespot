//go:build !linux

package mpris

import (
	librespot "github.com/devgianlu/go-librespot"
)

// NewServer creates a no-op mpris server to replace the equivalently named method in builds outside linux
func NewServer(logger librespot.Logger) (_ *DummyServer, err error) {
	logger.Warn("mpris was set to enabled although it is not included in this build")

	return &DummyServer{}, nil
}
