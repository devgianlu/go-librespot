//go:build test_unit

package mercury_test

import (
	"context"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
	"github.com/devgianlu/go-librespot/mercury"
	"github.com/stretchr/testify/require"
)

func TestRequestFailsWhenAccesspointIsClosed(t *testing.T) {
	accesspoint := ap.NewAccesspoint(&librespot.NullLogger{}, nil, "")
	accesspoint.Close()

	client := mercury.NewClient(&librespot.NullLogger{}, accesspoint)

	_, err := client.Request(context.Background(), "GET", "hm://test", nil, nil)
	require.ErrorIs(t, err, ap.ErrAccesspointClosed)
}
