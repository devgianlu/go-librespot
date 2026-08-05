//go:build test_unit

package spclient

import (
	"testing"

	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
)

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
