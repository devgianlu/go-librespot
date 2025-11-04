//go:build playplay

package playplay

import (
	"github.com/devgianlu/go-librespot/playplay/impl"
	"github.com/devgianlu/go-librespot/playplay/plugin"
)

var Plugin plugin.Interface = impl.Impl{}
