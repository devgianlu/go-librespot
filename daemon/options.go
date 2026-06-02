package daemon

import (
	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/mpris"
)

// Options bundles the dependencies a daemon needs at construction time.
type Options struct {
	Logger     librespot.Logger
	Config     *Config
	StateStore librespot.StateStore

	APIServer   ApiServer
	MediaPlayer mpris.Server
}
