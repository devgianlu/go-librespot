package audio

import (
	"context"
	"errors"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/ap"
)

func TestRequestFailsWhenAccesspointIsClosed(t *testing.T) {
	accesspoint := ap.NewAccesspoint(&librespot.NullLogger{}, nil, "")
	accesspoint.Close()

	provider := NewAudioKeyProvider(&librespot.NullLogger{}, accesspoint)

	_, err := provider.Request(context.Background(), nil, nil)
	if !errors.Is(err, ap.ErrAccesspointClosed) {
		t.Fatalf("expected ErrAccesspointClosed, got %v", err)
	}
}
