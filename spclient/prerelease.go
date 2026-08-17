package spclient

import (
	"context"
	"fmt"

	librespot "github.com/devgianlu/go-librespot"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	prereleasepb "github.com/devgianlu/go-librespot/proto/spotify/prerelease/extension"
)

// PrereleaseEntityUri resolves a spotify:prerelease: uri to the entity it
// stands for, normally the album, which is what can actually be played.
func (c *Spclient) PrereleaseEntityUri(ctx context.Context, uri string) (string, error) {
	var prerelease prereleasepb.Prerelease
	if err := c.ExtendedMetadataForUri(ctx, uri, extmetadatapb.ExtensionKind_PRERELEASE, &prerelease); err != nil {
		return "", fmt.Errorf("failed fetching prerelease extension for %s: %w", uri, err)
	}

	return prereleaseEntityUri(&prerelease)
}

// prereleaseEntityUri picks the playable uri out of a prerelease extension.
func prereleaseEntityUri(prerelease *prereleasepb.Prerelease) (string, error) {
	entityUri := prerelease.GetEntity().GetUri()
	if entityUri == "" {
		return "", fmt.Errorf("prerelease %q names no entity", prerelease.GetUri())
	}

	// The entity is what gets resolved and played, so a uri that says nothing
	// about its type is no better than none: it would be paged as a context of
	// unknown items.
	if librespot.InferSpotifyIdTypeFromContextUri(entityUri) == librespot.SpotifyIdTypeUnknown {
		return "", fmt.Errorf("prerelease %q names an unplayable entity %q (type %q)",
			prerelease.GetUri(), entityUri, prerelease.GetEntity().GetType())
	}

	return entityUri, nil
}
