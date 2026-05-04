package daemon

import (
	librespot "github.com/devgianlu/go-librespot"
)

// StateStore abstracts how a daemon persists its long-lived state.
type StateStore interface {
	Load() (*librespot.AppState, error)
	Save(*librespot.AppState) error
}
