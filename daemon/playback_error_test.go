//go:build test_unit

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/devgianlu/go-librespot/player"
	"github.com/devgianlu/go-librespot/spclient"
	"github.com/stretchr/testify/require"
)

func TestClassifyPlaybackError(t *testing.T) {
	status := func(code int) error {
		return fmt.Errorf("failed creating stream: %w", &librespot.HTTPStatusError{Endpoint: "storage resolve", StatusCode: code})
	}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"restricted media", fmt.Errorf("load: %w", librespot.ErrMediaRestricted), playbackErrorKindRestricted},
		{"refused audio key", &audio.KeyProviderError{Code: 1}, playbackErrorKindRestricted},
		{"no supported format", librespot.ErrNoSupportedFormats, playbackErrorKindUnsupported},
		{"rate limited", &spclient.RateLimitedError{}, playbackErrorKindRateLimited},
		{"too many requests", status(429), playbackErrorKindRateLimited},
		{"server error", status(503), playbackErrorKindServer},
		{"refused request", status(403), playbackErrorKindRejected},
		{"deadline", fmt.Errorf("load: %w", context.DeadlineExceeded), playbackErrorKindTimeout},
		{"dial timeout", &net.OpError{Op: "dial", Err: timeoutError{}}, playbackErrorKindTimeout},
		{"no route", &net.OpError{Op: "dial", Err: syscall.ENETUNREACH}, playbackErrorKindNetwork},
		{"dns", &net.DNSError{Err: "no such host", Name: "example.invalid"}, playbackErrorKindNetwork},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), playbackErrorKindNetwork},
		{"truncated", io.ErrUnexpectedEOF, playbackErrorKindNetwork},
		{"session lost", errSessionLost, playbackErrorKindNetwork},
		{"anything else", errors.New("decoder gave up"), playbackErrorKindUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, classifyPlaybackError(tc.err))
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// A stop the output device failed into is told apart from an ordinary one, and
// comes before the stopped it leads to.
func TestAFailedOutputReportsAPlaybackError(t *testing.T) {
	p := newTestAppPlayer(t)

	p.handlePlayerEvent(&player.Event{Type: player.EventTypeStop, Err: errors.New("device disappeared")})

	emitted := p.app.server.(*recordingApiServer).emitted
	require.Len(t, emitted, 2)
	require.Equal(t, ApiEventTypePlaybackError, emitted[0].Type)
	require.Equal(t, ApiEventTypeStopped, emitted[1].Type)

	data := emitted[0].Data.(ApiEventDataPlaybackError)
	require.Equal(t, playbackErrorStagePlayback, data.Stage)
	require.Equal(t, playbackErrorKindUnknown, data.Kind)
	require.Equal(t, "device disappeared", data.Message)
}

func TestAnOrdinaryStopReportsNoPlaybackError(t *testing.T) {
	p := newTestAppPlayer(t)

	p.handlePlayerEvent(&player.Event{Type: player.EventTypeStop})

	require.Equal(t, []ApiEventType{ApiEventTypeStopped}, apiEvents(p))
}

func TestACancelledLoadReportsNoPlaybackError(t *testing.T) {
	p := newTestAppPlayer(t)

	p.emitPlaybackError(playbackErrorStageLoad, "spotify:track:0", fmt.Errorf("load: %w", context.Canceled))

	require.Empty(t, apiEvents(p))
}
