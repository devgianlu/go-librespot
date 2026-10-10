//go:build test_unit

package spclient

import (
	"context"
	"io"
	"testing"

	librespot "github.com/devgianlu/go-librespot"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	"github.com/stretchr/testify/require"
)

// A Web API play naming "uris" sends a context with no uri and the tracks
// inline. It needs nothing resolved, so there is no spclient here to ask.
func TestNewContextResolverBareUris(t *testing.T) {
	spotCtx := &connectpb.Context{Pages: []*connectpb.ContextPage{{Tracks: []*connectpb.ContextTrack{
		{Uri: "spotify:track:1BdI8NEvZx61tnkbuAxC5x"},
		{Uri: "spotify:track:4u615SPuJfGtPaEUSKwuK4"},
	}}}}

	r, err := NewContextResolver(context.Background(), &librespot.NullLogger{}, nil, spotCtx)
	require.NoError(t, err)
	require.Equal(t, librespot.SpotifyIdTypeTrack, r.Type())
	require.Empty(t, r.Uri())

	_, err = NewContextResolver(context.Background(), &librespot.NullLogger{}, nil, &connectpb.Context{})
	require.Error(t, err, "a context with neither a uri nor items has nothing to play")
}

func TestHasResolvablePages(t *testing.T) {
	tests := []struct {
		name string
		ctx  *connectpb.Context
		want bool
	}{
		{
			name: "no pages",
			ctx:  &connectpb.Context{},
			want: false,
		},
		{
			// What a play command hands over for DJ on an already connected
			// device: a page shaped like a page but carrying nothing.
			name: "skeleton page",
			ctx: &connectpb.Context{
				Pages: []*connectpb.ContextPage{{}},
			},
			want: false,
		},
		{
			name: "page with tracks",
			ctx: &connectpb.Context{
				Pages: []*connectpb.ContextPage{{
					Tracks: []*connectpb.ContextTrack{{Uri: "spotify:track:2FY7b99s15jUprqC0M5NCT"}},
				}},
			},
			want: true,
		},
		{
			name: "empty page that says where to fetch itself",
			ctx: &connectpb.Context{
				Pages: []*connectpb.ContextPage{{PageUrl: "hm://something/page"}},
			},
			want: true,
		},
		{
			name: "empty page that names the next one",
			ctx: &connectpb.Context{
				Pages: []*connectpb.ContextPage{{NextPageUrl: "hm://something/next"}},
			},
			want: true,
		},
		{
			name: "one skeleton page among usable ones",
			ctx: &connectpb.Context{
				Pages: []*connectpb.ContextPage{
					{},
					{Tracks: []*connectpb.ContextTrack{{Uri: "spotify:track:2FY7b99s15jUprqC0M5NCT"}}},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasResolvablePages(tt.ctx); got != tt.want {
				t.Errorf("hasResolvablePages() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Context resolve answers a bare track uri with a context that has no pages.
// It used to reach Page and index Pages[-1] (devgianlu/go-librespot#422).
func TestContextResolverWithoutPages(t *testing.T) {
	r := &ContextResolver{log: &librespot.NullLogger{}, typ: librespot.SpotifyIdTypeTrack, ctx: &connectpb.Context{Uri: "spotify:track:197PHOBtgwBTSky9oye0u9"}}
	_, err := r.Page(context.Background(), 0)
	require.ErrorIs(t, err, io.EOF, "a resolver with no pages ends instead of panicking")

	for _, uri := range []string{"spotify:track:197PHOBtgwBTSky9oye0u9", "spotify:episode:512ojhOuo1ktJprKbVcKyQ"} {
		require.True(t, isItemUri(uri), uri)
	}
	for _, uri := range []string{"spotify:album:2up3OPMp9Tb4dAKM2erWXQ", "spotify:user:someone:collection", "spotify:show:5CfCWKI5pZ28U0uOzXkDHe"} {
		require.False(t, isItemUri(uri), uri)
	}
}
