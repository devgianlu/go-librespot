//go:build test_unit

package audio_test

import (
	"context"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/stretchr/testify/require"
)

func TestRequestFailsWhenAccesspointIsClosed(t *testing.T) {
	accesspoint := ap.NewAccesspoint(&librespot.NullLogger{}, nil, "")
	accesspoint.Close()

	provider := audio.NewAudioKeyProvider(&librespot.NullLogger{}, accesspoint)

	_, err := provider.Request(context.Background(), nil, nil)
	require.ErrorIs(t, err, ap.ErrAccesspointClosed)
}
