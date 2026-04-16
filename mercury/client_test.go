package mercury

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

	client := NewClient(&librespot.NullLogger{}, accesspoint)

	_, err := client.Request(context.Background(), "GET", "hm://test", nil, nil)
	if !errors.Is(err, ap.ErrAccesspointClosed) {
		t.Fatalf("expected ErrAccesspointClosed, got %v", err)
	}
}
