package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/spclient"
)

// Where in playback a playback_error happened.
const (
	playbackErrorStageLoad     = "load"
	playbackErrorStagePlayback = "playback"
	playbackErrorStageSession  = "session"
)

// What kind of failure a playback_error was, for clients that act on it without
// matching error text.
const (
	playbackErrorKindRestricted  = "restricted"
	playbackErrorKindUnsupported = "unsupported"
	playbackErrorKindRateLimited = "rate_limited"
	playbackErrorKindRejected    = "rejected"
	playbackErrorKindServer      = "server"
	playbackErrorKindTimeout     = "timeout"
	playbackErrorKindNetwork     = "network"
	playbackErrorKindUnknown     = "unknown"
)

var errSessionLost = errors.New("lost the connection to Spotify")

func classifyPlaybackError(err error) string {
	var keyErr *audio.KeyProviderError
	var rateErr *spclient.RateLimitedError
	var statusErr *librespot.HTTPStatusError
	var netErr net.Error

	switch {
	case errors.Is(err, librespot.ErrMediaRestricted), errors.As(err, &keyErr):
		return playbackErrorKindRestricted
	case errors.Is(err, librespot.ErrNoSupportedFormats):
		return playbackErrorKindUnsupported
	case errors.As(err, &rateErr):
		return playbackErrorKindRateLimited
	case errors.As(err, &statusErr):
		switch {
		case statusErr.StatusCode == http.StatusTooManyRequests:
			return playbackErrorKindRateLimited
		case statusErr.StatusCode >= 500:
			return playbackErrorKindServer
		case statusErr.StatusCode >= 400:
			return playbackErrorKindRejected
		default:
			return playbackErrorKindUnknown
		}
	case errors.Is(err, context.DeadlineExceeded):
		return playbackErrorKindTimeout
	case errors.As(err, &netErr):
		if netErr.Timeout() {
			return playbackErrorKindTimeout
		}
		return playbackErrorKindNetwork
	case errors.Is(err, errSessionLost),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ENETUNREACH),
		errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, io.ErrUnexpectedEOF):
		return playbackErrorKindNetwork
	default:
		return playbackErrorKindUnknown
	}
}

// emitPlaybackError tells API clients that playing uri failed. A load that was
// cancelled is not a failure: it was replaced, or the daemon is shutting down.
func (p *AppPlayer) emitPlaybackError(stage, uri string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}

	p.app.server.Emit(&ApiEvent{
		Type: ApiEventTypePlaybackError,
		Data: ApiEventDataPlaybackError{
			ContextUri: p.state.player.ContextUri,
			Uri:        uri,
			PlayOrigin: p.state.playOrigin(),
			Stage:      stage,
			Kind:       classifyPlaybackError(err),
			Unplayable: isUnplayableMedia(err),
			Message:    err.Error(),
		},
	})
}
