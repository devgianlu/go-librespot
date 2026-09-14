//go:build test_unit

package spclient

import (
	"testing"

	prereleasepb "github.com/devgianlu/go-librespot/proto/spotify/prerelease/extension"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

// TestPrereleaseEntityUri covers reading the playable entity out of the
// PRERELEASE extension, which is the only route to it: /context-resolve/v1 has
// no upstream service for the kind and fails for every prerelease id (#207).
func TestPrereleaseEntityUri(t *testing.T) {
	tests := []struct {
		name       string
		prerelease *prereleasepb.Prerelease
		want       string
		wantErr    string
	}{
		{
			name: "album",
			prerelease: &prereleasepb.Prerelease{
				Uri: "spotify:prerelease:6nQjPI2xUOZjJ7bJPMFxtF",
				Entity: &prereleasepb.Entity{
					Uri:  "spotify:album:5r36AJ6VOJtp00oxSkBZ5h",
					Type: ptr("ALBUM"),
					Name: "Unreleased",
				},
			},
			want: "spotify:album:5r36AJ6VOJtp00oxSkBZ5h",
		},
		{
			name:       "no entity",
			prerelease: &prereleasepb.Prerelease{Uri: "spotify:prerelease:6nQjPI2xUOZjJ7bJPMFxtF"},
			wantErr:    "names no entity",
		},
		{
			name: "entity without a uri",
			prerelease: &prereleasepb.Prerelease{
				Uri:    "spotify:prerelease:6nQjPI2xUOZjJ7bJPMFxtF",
				Entity: &prereleasepb.Entity{Name: "Unreleased"},
			},
			wantErr: "names no entity",
		},
		{
			// Rejected here rather than further in: an entity we cannot type is
			// one we cannot page as a context either.
			name: "entity of an unplayable kind",
			prerelease: &prereleasepb.Prerelease{
				Uri: "spotify:prerelease:6nQjPI2xUOZjJ7bJPMFxtF",
				Entity: &prereleasepb.Entity{
					Uri:  "spotify:image:ab67616d00001e02e89d",
					Type: ptr("IMAGE"),
				},
			},
			wantErr: "unplayable entity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := prereleaseEntityUri(tt.prerelease)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
